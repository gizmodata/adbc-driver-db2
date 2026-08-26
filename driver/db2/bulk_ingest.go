package db2

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/gizmodata/adbc-driver-db2/internal/drda"
)

// Statement options specific to ingest.
const (
	// OptionIngestVarcharLength sets the length used for VARCHAR /
	// VARBINARY columns created by bulk ingest. By default the driver
	// sizes each column from the first bound batch (twice the longest
	// value, at least 64, at most 32672). Db2's row-size limit depends
	// on the tablespace page size (about 4005 bytes on 4K pages), so a
	// fixed generous length can make CREATE TABLE fail on wide tables.
	OptionIngestVarcharLength = "adbc.db2.ingest.varchar_length"
	// OptionIngestBatchRows caps the rows pipelined per DRDA round trip
	// (default 1000).
	OptionIngestBatchRows = "adbc.db2.ingest.batch_rows"
)

const (
	minAutoVarcharLength   = 64
	maxVarcharLength       = 32672
	defaultIngestBatchRows = 1000
	// ingestBatchBytes bounds the approximate payload of one pipelined
	// transmission so wide rows don't build huge buffers.
	ingestBatchBytes = 8 << 20
)

// executeIngest implements ADBC bulk ingest: CREATE the target per the
// ingest mode, then stream the bound record(s) through a prepared
// INSERT, pipelining many rows per DRDA round trip.
func (s *statementImpl) executeIngest(ctx context.Context) (int64, error) {
	if s.targetTable == "" {
		return -1, errStatus(adbc.StatusInvalidState, "ingest: no target table set")
	}
	if s.bound == nil && s.boundStream == nil {
		return -1, errStatus(adbc.StatusInvalidState, "ingest: no record/stream bound")
	}
	defer s.clearBound()

	var schema *arrow.Schema
	if s.bound != nil {
		schema = s.bound.Schema()
	} else {
		schema = s.boundStream.Schema()
	}
	conn := s.conn
	// Peek at the first batch so string/binary columns can be sized.
	var first arrow.Record
	if s.bound != nil {
		first = s.bound
	} else if s.boundStream.Next() {
		first = s.boundStream.Record()
		first.Retain()
		defer first.Release()
	} else if err := s.boundStream.Err(); err != nil {
		return -1, errStatus(adbc.StatusIO, "ingest stream: %v", err)
	}
	if err := s.prepareIngestTarget(ctx, schema, first); err != nil {
		return -1, err
	}

	insert := buildInsertSQL(s.ingestTarget(), schema)
	ps, err := conn.conn.PrepareParams(ctx, insert)
	if err != nil {
		var ca *drda.SQLCA
		if errors.As(err, &ca) && ca.SQLCode == -204 {
			// Report the ODBC-style "table not found" SQLSTATE consumers
			// of ADBC ingest look for.
			e := adbc.Error{Code: adbc.StatusNotFound, Msg: "ingest: " + ca.Error(), VendorCode: ca.SQLCode}
			copy(e.SqlState[:], "42S02")
			return -1, e
		}
		return -1, fromDRDAError(err)
	}
	if len(ps.Params) != schema.NumFields() {
		return -1, errStatus(adbc.StatusInternal, "ingest: prepared INSERT describes %d parameters for %d columns", len(ps.Params), schema.NumFields())
	}

	batchRows := s.ingestBatchRows
	if batchRows <= 0 {
		batchRows = defaultIngestBatchRows
	}
	var total int64
	pump := func(rec arrow.Record) error {
		rows, err := recordRows(rec)
		if err != nil {
			return errStatus(adbc.StatusInvalidArgument, "ingest: %v", err)
		}
		for off := 0; off < len(rows); {
			end := off + batchRows
			if end > len(rows) {
				end = len(rows)
			}
			// Trim the batch further if it looks large.
			for end-off > 1 && approxRowBytes(rows[off:end]) > ingestBatchBytes {
				end = off + (end-off)/2
			}
			n, err := ps.ExecBatch(ctx, rows[off:end])
			if err != nil {
				return fromDRDAError(err)
			}
			total += n
			off = end
		}
		return nil
	}
	if s.bound != nil {
		if err := pump(s.bound); err != nil {
			return -1, err
		}
	}
	if s.boundStream != nil {
		if first != nil {
			if err := pump(first); err != nil {
				return -1, err
			}
		}
		for s.boundStream.Next() {
			if err := pump(s.boundStream.Record()); err != nil {
				return -1, err
			}
		}
		if err := s.boundStream.Err(); err != nil {
			return -1, errStatus(adbc.StatusIO, "ingest stream: %v", err)
		}
	}
	if err := conn.autoCommitIfNeeded(ctx); err != nil {
		return -1, err
	}
	return total, nil
}

func approxRowBytes(rows [][]drda.Value) int {
	n := 0
	for _, r := range rows {
		for _, v := range r {
			switch x := v.(type) {
			case string:
				n += len(x) + 3
			case []byte:
				n += len(x) + 3
			default:
				n += 12
			}
		}
	}
	return n
}

// ingestTarget renders the qualified target table name.
func (s *statementImpl) ingestTarget() string {
	if s.ingestTemporary {
		return "SESSION." + quoteIdent(s.targetTable)
	}
	if s.targetSchema != "" {
		return quoteIdent(s.targetSchema) + "." + quoteIdent(s.targetTable)
	}
	return quoteIdent(s.targetTable)
}

// prepareIngestTarget applies the ingest mode:
//
//	create        → CREATE TABLE (AlreadyExists if it exists)
//	append        → no DDL
//	replace       → DROP TABLE (if it exists) + CREATE TABLE
//	create_append → CREATE TABLE only if it does not exist
func (s *statementImpl) prepareIngestTarget(ctx context.Context, schema *arrow.Schema, sample arrow.Record) error {
	mode := s.ingestMode
	if mode == "" {
		mode = adbc.OptionValueIngestModeCreate
	}
	if mode == adbc.OptionValueIngestModeAppend {
		return nil
	}
	conn := s.conn
	lengths := varcharLengths(schema, sample, s.ingestVarcharLength)
	ddl, err := buildCreateTableSQL(s.ingestTarget(), schema, s.ingestTemporary, lengths)
	if err != nil {
		return errStatus(adbc.StatusInvalidArgument, "ingest: %v", err)
	}
	exists, err := s.targetExists(ctx)
	if err != nil {
		return err
	}
	switch mode {
	case adbc.OptionValueIngestModeCreate:
		if exists {
			// The ADBC spec reserves AlreadyExists for schema mismatches;
			// "create" on an existing table is a plain error.
			return adbc.Error{Code: adbc.StatusInternal, Msg: fmt.Sprintf("ingest: table %s already exists (use mode append, create_append or replace)", s.ingestTarget())}
		}
	case adbc.OptionValueIngestModeCreateAppend:
		if exists {
			return nil
		}
	case adbc.OptionValueIngestModeReplace:
		if exists {
			if _, err := conn.conn.ExecImmediate(ctx, "DROP TABLE "+s.ingestTarget()); err != nil {
				return fromDRDAError(err)
			}
		}
	}
	if _, err := conn.conn.ExecImmediate(ctx, ddl); err != nil {
		var ca *drda.SQLCA
		if errors.As(err, &ca) && ca.SQLCode == -601 {
			return adbc.Error{Code: adbc.StatusInternal, Msg: "ingest: " + ca.Error()}
		}
		return fromDRDAError(err)
	}
	return nil
}

// targetExists checks SYSCAT.TABLES for the ingest target. Declared
// temporary tables live in SESSION and are looked up via a probe query.
func (s *statementImpl) targetExists(ctx context.Context) (bool, error) {
	conn := s.conn
	if s.ingestTemporary {
		_, _, err := conn.conn.Describe(ctx, "SELECT * FROM "+s.ingestTarget()+" FETCH FIRST 1 ROW ONLY")
		if err == nil {
			return true, nil
		}
		var ca *drda.SQLCA
		if errors.As(err, &ca) && ca.SQLCode == -204 {
			return false, nil
		}
		return false, fromDRDAError(err)
	}
	schema := s.targetSchema
	if schema == "" {
		cur, err := conn.currentSchema(ctx)
		if err != nil {
			return false, err
		}
		schema = cur
	}
	_, rows, err := conn.queryAll(ctx, "SELECT 1 FROM SYSCAT.TABLES WHERE TABSCHEMA = "+sqlString(schema)+" AND TABNAME = "+sqlString(s.targetTable))
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// varcharLengths picks the declared length of each variable-length
// column: the explicit option when set, else sized from the sample.
func varcharLengths(schema *arrow.Schema, sample arrow.Record, fixed int) []int {
	out := make([]int, schema.NumFields())
	for i := range out {
		if fixed > 0 {
			out[i] = fixed
			continue
		}
		n := 0
		if sample != nil && i < int(sample.NumCols()) {
			n = maxValueLength(sample.Column(i))
		}
		n *= 2
		if n < minAutoVarcharLength {
			n = minAutoVarcharLength
		}
		if n > maxVarcharLength {
			n = maxVarcharLength
		}
		out[i] = n
	}
	return out
}

func maxValueLength(col arrow.Array) int {
	m := 0
	upd := func(n int) {
		if n > m {
			m = n
		}
	}
	switch a := col.(type) {
	case *array.String:
		for i := 0; i < a.Len(); i++ {
			upd(a.ValueLen(i))
		}
	case *array.LargeString:
		for i := 0; i < a.Len(); i++ {
			upd(len(a.Value(i)))
		}
	case *array.StringView:
		for i := 0; i < a.Len(); i++ {
			upd(len(a.Value(i)))
		}
	case *array.Binary:
		for i := 0; i < a.Len(); i++ {
			upd(a.ValueLen(i))
		}
	case *array.LargeBinary:
		for i := 0; i < a.Len(); i++ {
			upd(len(a.Value(i)))
		}
	case *array.BinaryView:
		for i := 0; i < a.Len(); i++ {
			upd(len(a.Value(i)))
		}
	case *array.Dictionary:
		return maxValueLength(a.Dictionary())
	}
	return m
}

func buildCreateTableSQL(target string, schema *arrow.Schema, temporary bool, lengths []int) (string, error) {
	var b strings.Builder
	if temporary {
		b.WriteString("DECLARE GLOBAL TEMPORARY TABLE ")
	} else {
		b.WriteString("CREATE TABLE ")
	}
	b.WriteString(target)
	b.WriteString(" (")
	for i, f := range schema.Fields() {
		if i > 0 {
			b.WriteString(", ")
		}
		typ, err := db2TypeForArrow(f, lengths[i])
		if err != nil {
			return "", fmt.Errorf("column %q: %w", f.Name, err)
		}
		b.WriteString(quoteIdent(f.Name))
		b.WriteByte(' ')
		b.WriteString(typ)
		if !f.Nullable {
			b.WriteString(" NOT NULL")
		}
	}
	b.WriteString(")")
	if temporary {
		b.WriteString(" ON COMMIT PRESERVE ROWS NOT LOGGED")
	}
	return b.String(), nil
}

func buildInsertSQL(target string, schema *arrow.Schema) string {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(target)
	b.WriteString(" (")
	for i, f := range schema.Fields() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(f.Name))
	}
	b.WriteString(") VALUES (")
	for i := range schema.Fields() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('?')
	}
	b.WriteString(")")
	return b.String()
}

func (s *statementImpl) boundSchema() *arrow.Schema {
	if s.bound != nil {
		return s.bound.Schema()
	}
	if s.boundStream != nil {
		return s.boundStream.Schema()
	}
	return nil
}

// prepareBound prepares s.sql for parameter execution. If Db2 cannot
// infer a marker's type (SQL0418N), the markers are rewritten as
// CAST(? AS <type>) from the bound Arrow schema and prepared again.
func (s *statementImpl) prepareBound(ctx context.Context, schema *arrow.Schema) (*drda.ParamStatement, error) {
	ps, err := s.conn.conn.PrepareParams(ctx, s.sql)
	if err == nil {
		return ps, nil
	}
	var ca *drda.SQLCA
	if errors.As(err, &ca) && ca.SQLCode == -418 && schema != nil {
		ps, err2 := s.conn.conn.PrepareParams(ctx, castParamMarkers(s.sql, schema))
		if err2 == nil {
			return ps, nil
		}
		err = err2
	}
	return nil, fromDRDAError(err)
}

// boundRows materializes the bound record or stream as parameter rows.
func (s *statementImpl) boundRows() ([][]drda.Value, error) {
	defer s.clearBound()
	var out [][]drda.Value
	if s.bound != nil {
		rows, err := recordRows(s.bound)
		if err != nil {
			return nil, errStatus(adbc.StatusInvalidArgument, "bind: %v", err)
		}
		out = append(out, rows...)
	}
	if s.boundStream != nil {
		for s.boundStream.Next() {
			rows, err := recordRows(s.boundStream.Record())
			if err != nil {
				return nil, errStatus(adbc.StatusInvalidArgument, "bind: %v", err)
			}
			out = append(out, rows...)
		}
		if err := s.boundStream.Err(); err != nil {
			return nil, errStatus(adbc.StatusIO, "bind stream: %v", err)
		}
	}
	return out, nil
}

// executeBoundUpdate runs the SQL once per bound row.
func (s *statementImpl) executeBoundUpdate(ctx context.Context) (int64, error) {
	schema := s.boundSchema()
	rows, err := s.boundRows()
	if err != nil {
		return -1, err
	}
	ps, err := s.prepareBound(ctx, schema)
	if err != nil {
		return -1, err
	}
	var total int64
	for off := 0; off < len(rows); off += defaultIngestBatchRows {
		end := off + defaultIngestBatchRows
		if end > len(rows) {
			end = len(rows)
		}
		n, err := ps.ExecBatch(ctx, rows[off:end])
		if err != nil {
			return -1, fromDRDAError(err)
		}
		total += n
	}
	if err := s.conn.autoCommitIfNeeded(ctx); err != nil {
		return -1, err
	}
	return total, nil
}

// executeBoundQuery opens a cursor with the (single) bound parameter
// row. Multiple bound rows are executed one after another and their
// results concatenated, as the ADBC specification permits.
func (s *statementImpl) executeBoundQuery(ctx context.Context) (array.RecordReader, int64, error) {
	bschema := s.boundSchema()
	rows, err := s.boundRows()
	if err != nil {
		return nil, -1, err
	}
	if len(rows) == 0 {
		return nil, -1, errStatus(adbc.StatusInvalidArgument, "bind: no parameter rows bound")
	}
	ps, err := s.prepareBound(ctx, bschema)
	if err != nil {
		return nil, -1, err
	}
	if len(rows) == 1 {
		q, err := ps.QueryParams(ctx, rows[0])
		if err != nil {
			return nil, -1, fromDRDAError(err)
		}
		return newStreamingRecordReader(ctx, s.conn, q, s.conn.cfg.batchSize), -1, nil
	}
	// Multi-row: materialize each execution (results are typically small
	// for parameterized lookups) and concatenate.
	var recs []arrow.Record
	var schema *arrow.Schema
	release := func() {
		for _, r := range recs {
			r.Release()
		}
	}
	for _, row := range rows {
		q, err := ps.QueryParams(ctx, row)
		if err != nil {
			release()
			return nil, -1, fromDRDAError(err)
		}
		rr := newStreamingRecordReader(ctx, s.conn, q, s.conn.cfg.batchSize)
		if schema == nil {
			schema = rr.Schema()
		}
		for rr.Next() {
			rec := rr.Record()
			rec.Retain()
			recs = append(recs, rec)
		}
		err = rr.Err()
		rr.Release()
		if err != nil {
			release()
			return nil, -1, err
		}
	}
	out, err := array.NewRecordReader(schema, recs)
	release()
	if err != nil {
		return nil, -1, errStatus(adbc.StatusInternal, "bind: %v", err)
	}
	return out, -1, nil
}
