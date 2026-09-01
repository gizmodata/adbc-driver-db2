package db2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

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
		var w io.Writer = os.Stderr
		var closer io.Closer
		if cfg.traceFile != "" {
			f, err := os.OpenFile(cfg.traceFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				conn.Close()
				return nil, errStatus(adbc.StatusInvalidArgument, "db2: open trace file: %v", err)
			}
			w, closer = f, f
		}
		var mu sync.Mutex
		conn.Trace = func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintf(w, "[adbc-db2 %s] "+format+"\n", append([]any{time.Now().Format("15:04:05.000")}, args...)...)
		}
		conn.TraceHex = cfg.traceHex
		conn.Trace("driver %s connected: server=%+v", driverVersion(), conn.Server)
		if closer != nil {
			c := &connectionImpl{db: d, conn: conn, alloc: d.alloc, cfg: cfg, autoCommit: true, traceCloser: closer}
			return c, nil
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
	db          *databaseImpl
	conn        *drda.Conn
	alloc       memory.Allocator
	cfg         *connConfig
	autoCommit  bool
	mu          sync.Mutex
	closed      bool
	traceCloser io.Closer
}

func (c *connectionImpl) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errStatus(adbc.StatusInvalidState, "connection already closed")
	}
	c.closed = true
	// Roll back anything uncommitted rather than leaving the UOW to the
	// server's discretion; ignore errors — the socket is going away.
	if !c.autoCommit {
		_ = c.conn.Rollback(context.Background())
	}
	err := c.conn.Close()
	if c.traceCloser != nil {
		_ = c.traceCloser.Close()
	}
	return err
}

func (c *connectionImpl) NewStatement() (adbc.Statement, error) {
	if c.closed {
		return nil, errStatus(adbc.StatusInvalidState, "connection is closed")
	}
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
	case adbc.OptionKeyIsolationLevel:
		var iso string
		switch value {
		case string(adbc.LevelDefault), string(adbc.LevelReadCommitted):
			iso = "CS"
		case string(adbc.LevelReadUncommitted):
			iso = "UR"
		case string(adbc.LevelRepeatableRead):
			iso = "RS"
		case string(adbc.LevelSerializable), string(adbc.LevelLinearizable), string(adbc.LevelSnapshot):
			iso = "RR"
		default:
			return errStatus(adbc.StatusNotImplemented, "isolation level %q is not supported by Db2", value)
		}
		_, err := c.conn.ExecImmediate(context.Background(), "SET CURRENT ISOLATION = "+iso)
		if err != nil {
			return fromDRDAError(err)
		}
		return c.autoCommitIfNeeded(context.Background())
	case adbc.OptionKeyReadOnly:
		switch value {
		case adbc.OptionValueEnabled, adbc.OptionValueDisabled:
			// Db2 has no connection-level read-only switch over DRDA; accept
			// the option so generic tooling works, without enforcement.
			return nil
		}
		return errStatus(adbc.StatusInvalidArgument, "invalid value %q for %s", value, key)
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
	case OptionBatchSize:
		return strconv.Itoa(c.cfg.batchSize), nil
	case OptionBatchBytes:
		return strconv.FormatInt(c.cfg.batchBytes, 10), nil
	case OptionSecurityMechanismActive:
		return strconv.Itoa(int(c.conn.SecurityMechanism())), nil
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
	conn                *connectionImpl
	alloc               memory.Allocator
	sql                 string
	targetTable         string
	targetSchema        string
	ingestMode          string
	ingestTemporary     bool
	ingestVarcharLength int
	ingestBatchRows     int
	closed              bool
	bound               arrow.Record
	boundStream         array.RecordReader
}

func (s *statementImpl) Close() error {
	if s.closed {
		return errStatus(adbc.StatusInvalidState, "statement already closed")
	}
	s.closed = true
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

func (s *statementImpl) SetSqlQuery(sql string) error { s.sql = addDummyFrom(sql); return nil }

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
	case adbc.OptionValueIngestTemporary:
		switch value {
		case adbc.OptionValueEnabled:
			s.ingestTemporary = true
		case adbc.OptionValueDisabled:
			s.ingestTemporary = false
		default:
			return errStatus(adbc.StatusInvalidArgument, "invalid value %q for %s", value, key)
		}
	case OptionIngestVarcharLength:
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return errStatus(adbc.StatusInvalidArgument, "invalid value %q for %s", value, key)
		}
		s.ingestVarcharLength = n
	case OptionIngestBatchRows:
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return errStatus(adbc.StatusInvalidArgument, "invalid value %q for %s", value, key)
		}
		s.ingestBatchRows = n
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
	if s.targetTable != "" && (s.bound != nil || s.boundStream != nil) {
		n, err := s.executeIngest(ctx)
		if err != nil {
			return nil, -1, err
		}
		rr, err := array.NewRecordReader(arrow.NewSchema([]arrow.Field{}, nil), nil)
		if err != nil {
			return nil, -1, err
		}
		return rr, n, nil
	}
	if s.sql == "" {
		return nil, -1, errStatus(adbc.StatusInvalidState, "ExecuteQuery: no SQL set")
	}
	if s.bound != nil || s.boundStream != nil {
		return s.executeBoundQuery(ctx)
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
	if len(q.Columns) != len(q.Fields) {
		_ = q.Close(ctx)
		return nil, -1, errStatus(adbc.StatusInternal,
			"db2: result descriptor mismatch: SQLDARD describes %d columns but QRYDSC has %d fields (please report with adbc.db2.trace=hex)",
			len(q.Columns), len(q.Fields))
	}
	return newStreamingRecordReader(ctx, s.conn, q, s.conn.cfg.batchSize), -1, nil
}

func (s *statementImpl) ExecuteUpdate(ctx context.Context) (int64, error) {
	if s.targetTable != "" && (s.bound != nil || s.boundStream != nil) {
		return s.executeIngest(ctx)
	}
	if s.sql == "" {
		return -1, errStatus(adbc.StatusInvalidState, "ExecuteUpdate: no SQL set")
	}
	if s.bound != nil || s.boundStream != nil {
		return s.executeBoundUpdate(ctx)
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
	if s.sql == "" {
		return nil, errStatus(adbc.StatusInvalidState, "GetParameterSchema: no SQL set")
	}
	_, params, err := s.conn.conn.Describe(context.Background(), s.sql)
	if err != nil {
		var ca *drda.SQLCA
		if errors.As(err, &ca) && ca.SQLCode == -418 {
			// SQL0418N: a parameter marker whose type cannot be inferred
			// (e.g. "SELECT ?"). The parameter schema is unknown.
			return nil, nil
		}
		return nil, fromDRDAError(err)
	}
	fields := make([]arrow.Field, len(params))
	for i, p := range params {
		f := arrowFieldFor(p)
		f.Name = strconv.Itoa(i)
		f.Nullable = true
		fields[i] = f
	}
	return arrow.NewSchema(fields, nil), nil
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
