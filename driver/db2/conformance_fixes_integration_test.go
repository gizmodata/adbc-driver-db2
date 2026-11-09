package db2

// Regression tests for the driver bugs the adbc-drivers.org validation suite
// (validation/) found, plus the cases around them the suite doesn't reach
// (NOT NULL LOB columns, FOR BIT DATA). They need a live Db2 (DB2_HOST).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

func queryOne(t *testing.T, conn *connectionImpl, sql string) arrow.Record {
	t.Helper()
	stmt, err := conn.NewStatement()
	require.NoError(t, err)
	t.Cleanup(func() { stmt.Close() })
	require.NoError(t, stmt.SetSqlQuery(sql))
	rr, _, err := stmt.ExecuteQuery(context.Background())
	require.NoError(t, err)
	recs := readAll(t, rr)
	require.Len(t, recs, 1)
	return recs[0]
}

func execSQL(t *testing.T, conn *connectionImpl, sql string) {
	t.Helper()
	stmt, err := conn.NewStatement()
	require.NoError(t, err)
	defer stmt.Close()
	require.NoError(t, stmt.SetSqlQuery(sql))
	_, err = stmt.ExecuteUpdate(context.Background())
	require.NoError(t, err)
}

func uniqueTable(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// LOB values in NOT NULL columns have no null-indicator byte in their
// EXTDTA; the driver used to strip one anyway (dropping the first data
// byte), and empty LOBs (no EXTDTA at all) came back as garbage or failed.
func TestLOBRoundTrip(t *testing.T) {
	conn := openConn(t)
	rec := queryOne(t, conn, `SELECT CAST(BX'deadbeef' AS BLOB) AS B, CAST(BX'' AS BLOB) AS BE,
		CAST('abc' AS CLOB) AS C, CAST('' AS CLOB) AS CE, CAST(BX'00' AS BLOB) AS B0
		FROM SYSIBM.SYSDUMMY1`)
	require.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, rec.Column(0).(*array.Binary).Value(0))
	require.Equal(t, []byte{}, rec.Column(1).(*array.Binary).Value(0))
	require.False(t, rec.Column(1).IsNull(0))
	require.Equal(t, "abc", rec.Column(2).(*array.String).Value(0))
	require.Equal(t, "", rec.Column(3).(*array.String).Value(0))
	require.False(t, rec.Column(3).IsNull(0))
	require.Equal(t, []byte{0x00}, rec.Column(4).(*array.Binary).Value(0))

	table := uniqueTable("ADBC_LOB")
	execSQL(t, conn, `CREATE TABLE `+table+` (I INT, A BLOB, B BLOB NOT NULL, C CLOB, D CLOB NOT NULL)`)
	t.Cleanup(func() { execSQL(t, conn, "DROP TABLE "+table) })
	execSQL(t, conn, `INSERT INTO `+table+` VALUES (1, BX'deadbeef', BX'cafe', 'abc', 'xyz'),
		(2, NULL, BX'', NULL, ''), (3, BX'', BX'00', '', 'q')`)
	rec = queryOne(t, conn, "SELECT A, B, C, D FROM "+table+" ORDER BY I")
	a, b := rec.Column(0).(*array.Binary), rec.Column(1).(*array.Binary)
	c, d := rec.Column(2).(*array.String), rec.Column(3).(*array.String)
	require.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, a.Value(0))
	require.True(t, a.IsNull(1))
	require.Equal(t, []byte{}, a.Value(2))
	require.Equal(t, [][]byte{{0xca, 0xfe}, {}, {0x00}}, [][]byte{b.Value(0), b.Value(1), b.Value(2)})
	require.Equal(t, "abc", c.Value(0))
	require.True(t, c.IsNull(1))
	require.Equal(t, "", c.Value(2))
	require.Equal(t, []string{"xyz", "", "q"}, []string{d.Value(0), d.Value(1), d.Value(2)})
}

// FOR BIT DATA columns hold bytes; as Arrow strings they carried invalid
// UTF-8.
func TestForBitDataIsBinary(t *testing.T) {
	conn := openConn(t)
	rec := queryOne(t, conn, `SELECT CAST(X'00ff' AS VARCHAR(4) FOR BIT DATA) AS V,
		CAST(X'00ff' AS CHAR(2) FOR BIT DATA) AS C, CAST('ok' AS VARCHAR(4)) AS S
		FROM SYSIBM.SYSDUMMY1`)
	require.Equal(t, arrow.BinaryTypes.Binary, rec.Schema().Field(0).Type)
	require.Equal(t, arrow.BinaryTypes.Binary, rec.Schema().Field(1).Type)
	require.Equal(t, arrow.BinaryTypes.String, rec.Schema().Field(2).Type)
	require.Equal(t, []byte{0x00, 0xff}, rec.Column(0).(*array.Binary).Value(0))
	require.Equal(t, []byte{0x00, 0xff}, rec.Column(1).(*array.Binary).Value(0))
	typ, _ := rec.Schema().Field(0).Metadata.GetValue(metaDb2Type)
	require.Equal(t, "VARCHAR(4) FOR BIT DATA", typ)
}

func bindInsert(t *testing.T, conn *connectionImpl, sql string, rec arrow.Record) error {
	t.Helper()
	stmt, err := conn.NewStatement()
	require.NoError(t, err)
	defer stmt.Close()
	require.NoError(t, stmt.SetSqlQuery(sql))
	require.NoError(t, stmt.Bind(context.Background(), rec))
	_, err = stmt.ExecuteUpdate(context.Background())
	return err
}

// A time-zone-aware timestamp is an instant: it is stored as UTC whatever
// its display zone (it used to be stored as wall time in that zone, so
// 9999-12-31 UTC overflowed in Asia/Tokyo).
func TestBindTimestampTZAsUTC(t *testing.T) {
	conn := openConn(t)
	table := uniqueTable("ADBC_TSTZ")
	execSQL(t, conn, "CREATE TABLE "+table+" (A TIMESTAMP(6), B TIMESTAMP(6))")
	t.Cleanup(func() { execSQL(t, conn, "DROP TABLE "+table) })

	const instant = arrow.Timestamp(1684158330123456) // 2023-05-15 13:45:30.123456 UTC
	const last = arrow.Timestamp(253402300799999999)  // 9999-12-31 23:59:59.999999 UTC
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "a", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, Nullable: true},
		{Name: "b", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "Asia/Tokyo"}, Nullable: true},
	}, nil)
	bld := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	defer bld.Release()
	bld.Field(0).(*array.TimestampBuilder).AppendValues([]arrow.Timestamp{instant, last}, nil)
	bld.Field(1).(*array.TimestampBuilder).AppendValues([]arrow.Timestamp{instant, last}, nil)
	rec := bld.NewRecord()
	defer rec.Release()
	require.NoError(t, bindInsert(t, conn, "INSERT INTO "+table+" VALUES (?, ?)", rec))

	got := queryOne(t, conn, "SELECT VARCHAR(A), VARCHAR(B) FROM "+table+" ORDER BY A")
	for col := 0; col < 2; col++ {
		s := got.Column(col).(*array.String)
		require.Equal(t, "2023-05-15-13.45.30.123456", s.Value(0), "column %d", col)
		require.Equal(t, "9999-12-31-23.59.59.999999", s.Value(1), "column %d", col)
	}
}

// Db2 TIME has whole-second precision: a fractional second is refused,
// not silently dropped. Whole seconds and NULLs still bind.
func TestBindTimeFractionRefused(t *testing.T) {
	conn := openConn(t)
	table := uniqueTable("ADBC_TIME")
	execSQL(t, conn, "CREATE TABLE "+table+" (T TIME)")
	t.Cleanup(func() { execSQL(t, conn, "DROP TABLE "+table) })

	bind := func(values []arrow.Time64, valid []bool) error {
		sc := arrow.NewSchema([]arrow.Field{{Name: "t", Type: arrow.FixedWidthTypes.Time64us, Nullable: true}}, nil)
		bld := array.NewRecordBuilder(memory.DefaultAllocator, sc)
		defer bld.Release()
		bld.Field(0).(*array.Time64Builder).AppendValues(values, valid)
		rec := bld.NewRecord()
		defer rec.Release()
		return bindInsert(t, conn, "INSERT INTO "+table+" VALUES (?)", rec)
	}
	// 13:45:31 and a NULL whose slot holds a fractional value.
	require.NoError(t, bind([]arrow.Time64{49531000000, 1}, []bool{true, false}))
	err := bind([]arrow.Time64{49531123456}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "whole-second precision")

	got := queryOne(t, conn, "SELECT COUNT(*), COUNT(T) FROM "+table)
	require.EqualValues(t, 2, got.Column(0).(*array.Int32).Value(0))
	require.EqualValues(t, 1, got.Column(1).(*array.Int32).Value(0))
}

func ingest(t *testing.T, conn *connectionImpl, table, mode string, rec arrow.Record) (int64, error) {
	t.Helper()
	stmt, err := conn.NewStatement()
	require.NoError(t, err)
	defer stmt.Close()
	require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestTargetTable, table))
	require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestMode, mode))
	require.NoError(t, stmt.Bind(context.Background(), rec))
	return stmt.ExecuteUpdate(context.Background())
}

func requireStatus(t *testing.T, err error, code adbc.Status) {
	t.Helper()
	var ae adbc.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, code, ae.Code, ae.Msg)
}

// Appending data with a column the table lacks is a schema mismatch
// (ALREADY_EXISTS); a missing table is NOT_FOUND; a subset of the columns
// is fine.
func TestIngestAppendSchemaMismatch(t *testing.T) {
	conn := openConn(t)
	table := uniqueTable("adbc_append")
	t.Cleanup(func() { execSQL(t, conn, `DROP TABLE "`+table+`"`) })
	mk := func(names ...string) arrow.Record {
		fields := make([]arrow.Field, len(names))
		for i, n := range names {
			fields[i] = arrow.Field{Name: n, Type: arrow.PrimitiveTypes.Int64, Nullable: true}
		}
		bld := array.NewRecordBuilder(memory.DefaultAllocator, arrow.NewSchema(fields, nil))
		defer bld.Release()
		for i := range names {
			bld.Field(i).(*array.Int64Builder).Append(1)
		}
		return bld.NewRecord()
	}
	ab, abc, a := mk("a", "b"), mk("a", "b", "c"), mk("a")
	defer ab.Release()
	defer abc.Release()
	defer a.Release()

	_, err := ingest(t, conn, table+"_missing", adbc.OptionValueIngestModeAppend, ab)
	requireStatus(t, err, adbc.StatusNotFound)

	_, err = ingest(t, conn, table, adbc.OptionValueIngestModeCreate, ab)
	require.NoError(t, err)
	_, err = ingest(t, conn, table, adbc.OptionValueIngestModeCreate, ab)
	requireStatus(t, err, adbc.StatusAlreadyExists)
	_, err = ingest(t, conn, table, adbc.OptionValueIngestModeAppend, abc)
	requireStatus(t, err, adbc.StatusAlreadyExists)
	_, err = ingest(t, conn, table, adbc.OptionValueIngestModeCreateAppend, abc)
	requireStatus(t, err, adbc.StatusAlreadyExists)
	n, err := ingest(t, conn, table, adbc.OptionValueIngestModeAppend, a)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

// Db2 has no negative decimal scale: decimal(2,-2) becomes DECIMAL(4,0)
// holding the actual values (unscaled 99 at scale -2 is 9900); values
// longer than a VARCHAR get a CLOB/BLOB column instead of SQL0302N.
func TestIngestNegativeScaleAndLongValues(t *testing.T) {
	conn := openConn(t)
	table := uniqueTable("adbc_wide")
	t.Cleanup(func() { execSQL(t, conn, `DROP TABLE "`+table+`"`) })
	long := strings.Repeat("x", 40000)
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "d", Type: &arrow.Decimal128Type{Precision: 2, Scale: -2}, Nullable: true},
		{Name: "s", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "b", Type: arrow.BinaryTypes.Binary, Nullable: true},
	}, nil)
	bld := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	defer bld.Release()
	bld.Field(0).(*array.Decimal128Builder).AppendValues([]decimal128.Num{decimal128.FromI64(99), decimal128.FromI64(-10)}, nil)
	bld.Field(1).(*array.StringBuilder).AppendValues([]string{long, "short"}, nil)
	bld.Field(2).(*array.BinaryBuilder).AppendValues([][]byte{[]byte(long), {1}}, nil)
	rec := bld.NewRecord()
	defer rec.Release()
	_, err := ingest(t, conn, table, adbc.OptionValueIngestModeCreate, rec)
	require.NoError(t, err)

	got := queryOne(t, conn, `SELECT VARCHAR("d"), LENGTH("s"), LENGTH("b") FROM "`+table+`" ORDER BY "d"`)
	require.Equal(t, "-1000", strings.TrimSpace(got.Column(0).(*array.String).Value(0)))
	require.Equal(t, "9900", strings.TrimSpace(got.Column(0).(*array.String).Value(1)))
	require.EqualValues(t, 40000, got.Column(1).(*array.Int32).Value(1))
	require.EqualValues(t, 40000, got.Column(2).(*array.Int32).Value(1))
}

// Check constraints are listed by GetObjects (they are not in
// SYSCAT.KEYCOLUSE, which is all the driver used to read).
func TestGetObjectsCheckConstraints(t *testing.T) {
	conn := openConn(t)
	table := uniqueTable("ADBC_CHECK")
	execSQL(t, conn, "CREATE TABLE "+table+` (A INT, B INT, CONSTRAINT CK_A CHECK (A > 0), CONSTRAINT CK_AB CHECK (A + B > 0))`)
	t.Cleanup(func() { execSQL(t, conn, "DROP TABLE "+table) })
	rr, err := conn.GetObjects(context.Background(), adbc.ObjectDepthAll, nil, nil, &table, nil, nil)
	require.NoError(t, err)
	recs := readAll(t, rr)
	require.Len(t, recs, 1)
	js, err := recs[0].MarshalJSON()
	require.NoError(t, err)
	out := string(js)
	require.Contains(t, out, `{"constraint_column_names":["A"],"constraint_column_usage":null,"constraint_name":"CK_A","constraint_type":"CHECK"}`)
	require.Contains(t, out, `{"constraint_column_names":["A","B"],"constraint_column_usage":null,"constraint_name":"CK_AB","constraint_type":"CHECK"}`)
}

// ADBC: an unknown option is NOT_IMPLEMENTED, a schema that does not
// exist is NOT_FOUND, and the Arrow version is a real release.
func TestOptionAndInfoContracts(t *testing.T) {
	drv := NewDriver(memory.DefaultAllocator)
	db, err := drv.NewDatabase(map[string]string{OptionURI: testURI(t)})
	require.NoError(t, err)
	defer db.Close()
	requireStatus(t, db.SetOptions(map[string]string{"this_option_does_not_exist": "x"}), adbc.StatusNotImplemented)
	require.NoError(t, db.SetOptions(map[string]string{adbc.OptionKeyAutoCommit: adbc.OptionValueEnabled}))
	_, err = drv.NewDatabase(map[string]string{OptionURI: testURI(t), "this_option_does_not_exist": "x"})
	if err == nil {
		db2, _ := drv.NewDatabase(map[string]string{OptionURI: testURI(t), "this_option_does_not_exist": "x"})
		_, err = db2.Open(context.Background())
	}
	requireStatus(t, err, adbc.StatusNotImplemented)

	conn := openConn(t)
	requireStatus(t, conn.SetOption(adbc.OptionKeyCurrentDbSchema, "THIS_SCHEMA_DOES_NOT_EXIST"), adbc.StatusNotFound)
	require.NoError(t, conn.SetOption(adbc.OptionKeyCurrentDbSchema, "SYSCAT"))
	cur, err := conn.GetOption(adbc.OptionKeyCurrentDbSchema)
	require.NoError(t, err)
	require.Equal(t, "SYSCAT", cur)

	// go test binaries carry no dependency build info, so this sees the
	// "v18" fallback; python/tests/test_smoke.py checks the real release
	// through the c-shared library.
	require.Regexp(t, `^v18(\.\d+\.\d+)?$`, arrowVersion())
}
