package db2

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// DriverName / VendorName are reported via GetInfo.
const (
	DriverName = "ADBC Db2 Driver - Go"
	VendorName = "IBM Db2"
)

// NewDriver returns a Db2 ADBC driver.
func NewDriver(alloc memory.Allocator) adbc.Driver {
	if alloc == nil {
		alloc = memory.NewGoAllocator()
	}
	return &driverImpl{alloc: alloc}
}

type driverImpl struct {
	alloc memory.Allocator
}

func (d *driverImpl) NewDatabase(opts map[string]string) (adbc.Database, error) {
	return d.NewDatabaseWithContext(context.Background(), opts)
}

func (d *driverImpl) NewDatabaseWithContext(_ context.Context, opts map[string]string) (adbc.Database, error) {
	return &databaseImpl{alloc: d.alloc, opts: cloneMap(opts)}, nil
}

type databaseImpl struct {
	alloc memory.Allocator
	mu    sync.Mutex
	opts  map[string]string
}

func (d *databaseImpl) SetOptions(opts map[string]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, v := range opts {
		d.opts[k] = v
	}
	return nil
}

func (d *databaseImpl) Open(ctx context.Context) (adbc.Connection, error) {
	d.mu.Lock()
	opts := cloneMap(d.opts)
	d.mu.Unlock()
	cfg, err := parseOptions(opts)
	if err != nil {
		return nil, err
	}
	conn, err := drda.Dial(ctx, cfg.drda)
	if err != nil {
		return nil, fromDRDAError(err)
	}
	if cfg.trace {
		conn.Trace = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[adbc-db2] "+format+"\n", args...)
		}
	}
	return &connectionImpl{
		db:         d,
		conn:       conn,
		alloc:      d.alloc,
		cfg:        cfg,
		autoCommit: true,
	}, nil
}

func (d *databaseImpl) Close() error { return nil }

// connectionImpl implements adbc.Connection over one DRDA session.
//
// Transaction model: DRDA always runs inside a unit of work. With
// autocommit on (the ADBC default) the driver issues RDBCMM after every
// statement completes — for queries, once the result set is fully
// consumed or released — mirroring what JCC/CLI do. With autocommit
// off, nothing is committed until Commit/Rollback.
type connectionImpl struct {
	db         *databaseImpl
	conn       *drda.Conn
	alloc      memory.Allocator
	cfg        *connConfig
	autoCommit bool
	mu         sync.Mutex
}

func (c *connectionImpl) Close() error {
	// Roll back anything uncommitted rather than leaving the UOW to the
	// server's discretion; ignore errors — the socket is going away.
	if !c.autoCommit {
		_ = c.conn.Rollback(context.Background())
	}
	return c.conn.Close()
}

func (c *connectionImpl) NewStatement() (adbc.Statement, error) {
	return &statementImpl{conn: c, alloc: c.alloc}, nil
}

func (c *connectionImpl) GetInfo(ctx context.Context, codes []adbc.InfoCode) (array.RecordReader, error) {
	return c.getInfoImpl(ctx, codes)
}

func (c *connectionImpl) GetObjects(ctx context.Context, depth adbc.ObjectDepth, catalog, dbSchema, tableName *string, columnName *string, tableTypes []string) (array.RecordReader, error) {
	return c.getObjectsImpl(ctx, depth, catalog, dbSchema, tableName, columnName, tableTypes)
}

func (c *connectionImpl) GetTableSchema(ctx context.Context, catalog, dbSchema *string, tableName string) (*arrow.Schema, error) {
	return c.getTableSchemaImpl(ctx, catalog, dbSchema, tableName)
}

func (c *connectionImpl) GetTableTypes(ctx context.Context) (array.RecordReader, error) {
	return c.getTableTypesImpl(ctx)
}

func (c *connectionImpl) Commit(ctx context.Context) error {
	if c.autoCommit {
		return errStatus(adbc.StatusInvalidState, "Commit called while autocommit is enabled")
	}
	return fromDRDAError(c.conn.Commit(ctx))
}

func (c *connectionImpl) Rollback(ctx context.Context) error {
	if c.autoCommit {
		return errStatus(adbc.StatusInvalidState, "Rollback called while autocommit is enabled")
	}
	return fromDRDAError(c.conn.Rollback(ctx))
}

func (c *connectionImpl) SetOption(key, value string) error {
	switch key {
	case adbc.OptionKeyAutoCommit:
		switch value {
		case adbc.OptionValueEnabled:
			if !c.autoCommit {
				// Turning autocommit on commits the pending unit of work.
				if err := c.conn.Commit(context.Background()); err != nil {
					return fromDRDAError(err)
				}
			}
			c.autoCommit = true
		case adbc.OptionValueDisabled:
			c.autoCommit = false
		default:
			return errStatus(adbc.StatusInvalidArgument, "unknown value %q for %s", value, key)
		}
		return nil
	case adbc.OptionKeyCurrentDbSchema:
		_, err := c.conn.ExecImmediate(context.Background(), "SET CURRENT SCHEMA "+quoteIdent(value))
		if err != nil {
			return fromDRDAError(err)
		}
		return c.autoCommitIfNeeded(context.Background())
	}
	return errStatus(adbc.StatusNotImplemented, "unknown connection option %q", key)
}

func (c *connectionImpl) GetOption(key string) (string, error) {
	switch key {
	case adbc.OptionKeyAutoCommit:
		if c.autoCommit {
			return adbc.OptionValueEnabled, nil
		}
		return adbc.OptionValueDisabled, nil
	case adbc.OptionKeyCurrentCatalog:
		return c.conn.Database(), nil
	case adbc.OptionKeyCurrentDbSchema:
		return c.currentSchema(context.Background())
	}
	return "", errStatus(adbc.StatusNotFound, "unknown connection option %q", key)
}

func (c *connectionImpl) ReadPartition(context.Context, []byte) (array.RecordReader, error) {
	return nil, errStatus(adbc.StatusNotImplemented, "ReadPartition")
}

// autoCommitIfNeeded ends the unit of work when autocommit is on.
func (c *connectionImpl) autoCommitIfNeeded(ctx context.Context) error {
	if !c.autoCommit {
		return nil
	}
	return fromDRDAError(c.conn.Commit(ctx))
}

// queryFinished is called by the record reader when a result set is
// exhausted or released. ok=false means the reader stopped on an error.
func (c *connectionImpl) queryFinished(ok bool) {
	if c.autoCommit {
		_ = c.conn.Commit(context.Background())
	}
}

// statementImpl implements adbc.Statement.
type statementImpl struct {
	conn         *connectionImpl
	alloc        memory.Allocator
	sql          string
	targetTable  string
	targetSchema string
	ingestMode   string
	bound        arrow.Record
	boundStream  array.RecordReader
}

func (s *statementImpl) Close() error {
	s.clearBound()
	return nil
}

func (s *statementImpl) clearBound() {
	if s.bound != nil {
		s.bound.Release()
		s.bound = nil
	}
	if s.boundStream != nil {
		s.boundStream.Release()
		s.boundStream = nil
	}
}

func (s *statementImpl) SetSqlQuery(sql string) error { s.sql = sql; return nil }

func (s *statementImpl) SetOption(key, value string) error {
	switch key {
	case adbc.OptionKeyIngestTargetTable:
		s.targetTable = value
	case adbc.OptionValueIngestTargetDBSchema:
		s.targetSchema = value
	case adbc.OptionKeyIngestMode:
		switch value {
		case adbc.OptionValueIngestModeCreate, adbc.OptionValueIngestModeAppend,
			adbc.OptionValueIngestModeReplace, adbc.OptionValueIngestModeCreateAppend:
			s.ingestMode = value
		default:
			return errStatus(adbc.StatusInvalidArgument, "unknown ingest mode %q", value)
		}
	default:
		return errStatus(adbc.StatusNotImplemented, "unknown statement option %q", key)
	}
	return nil
}

func (s *statementImpl) SetSubstraitPlan([]byte) error {
	return errStatus(adbc.StatusNotImplemented, "Substrait")
}

func (s *statementImpl) Prepare(context.Context) error { return nil }

func (s *statementImpl) ExecuteQuery(ctx context.Context) (array.RecordReader, int64, error) {
	if s.sql == "" {
		return nil, -1, errStatus(adbc.StatusInvalidState, "ExecuteQuery: no SQL set")
	}
	if s.bound != nil || s.boundStream != nil {
		return nil, -1, errStatus(adbc.StatusNotImplemented, "parameter binding is not implemented yet")
	}
	q, err := s.conn.conn.Query(ctx, s.sql)
	if err != nil {
		return nil, -1, fromDRDAError(err)
	}
	if !q.IsResultSet() {
		// Statement ran but produced no cursor (DDL/DML via ExecuteQuery):
		// return an empty reader with an empty schema.
		if err := s.conn.autoCommitIfNeeded(ctx); err != nil {
			return nil, -1, err
		}
		schema := arrow.NewSchema([]arrow.Field{}, nil)
		rr, err := array.NewRecordReader(schema, nil)
		if err != nil {
			return nil, -1, err
		}
		return rr, q.Result.RowsAffected, nil
	}
	return newStreamingRecordReader(ctx, s.conn, q, s.conn.cfg.batchSize), -1, nil
}

func (s *statementImpl) ExecuteUpdate(ctx context.Context) (int64, error) {
	if s.targetTable != "" && (s.bound != nil || s.boundStream != nil) {
		return -1, errStatus(adbc.StatusNotImplemented, "bulk ingest is not implemented yet")
	}
	if s.sql == "" {
		return -1, errStatus(adbc.StatusInvalidState, "ExecuteUpdate: no SQL set")
	}
	res, err := s.conn.conn.ExecImmediate(ctx, s.sql)
	if err != nil {
		return -1, fromDRDAError(err)
	}
	if err := s.conn.autoCommitIfNeeded(ctx); err != nil {
		return -1, err
	}
	return res.RowsAffected, nil
}

func (s *statementImpl) ExecuteSchema(ctx context.Context) (*arrow.Schema, error) {
	if s.sql == "" {
		return nil, errStatus(adbc.StatusInvalidState, "ExecuteSchema: no SQL set")
	}
	cols, _, err := s.conn.conn.Describe(ctx, s.sql)
	if err != nil {
		return nil, fromDRDAError(err)
	}
	return schemaFor(cols), nil
}

func (s *statementImpl) GetParameterSchema() (*arrow.Schema, error) {
	return nil, errStatus(adbc.StatusNotImplemented, "GetParameterSchema")
}

func (s *statementImpl) Bind(_ context.Context, rec arrow.Record) error {
	s.clearBound()
	rec.Retain()
	s.bound = rec
	return nil
}

func (s *statementImpl) BindStream(_ context.Context, rr array.RecordReader) error {
	s.clearBound()
	rr.Retain()
	s.boundStream = rr
	return nil
}

func (s *statementImpl) ExecutePartitions(context.Context) (*arrow.Schema, adbc.Partitions, int64, error) {
	return nil, adbc.Partitions{}, -1, errStatus(adbc.StatusNotImplemented, "ExecutePartitions")
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Compile-time interface checks.
var (
	_ adbc.Driver                 = (*driverImpl)(nil)
	_ adbc.Database               = (*databaseImpl)(nil)
	_ adbc.Connection             = (*connectionImpl)(nil)
	_ adbc.Statement              = (*statementImpl)(nil)
	_ adbc.StatementExecuteSchema = (*statementImpl)(nil)
)
