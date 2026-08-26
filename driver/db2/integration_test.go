package db2

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

func testURI(t *testing.T) string {
	t.Helper()
	host := os.Getenv("DB2_HOST")
	if host == "" {
		t.Skip("DB2_HOST not set")
	}
	port := os.Getenv("DB2_PORT")
	if port == "" {
		port = "50000"
	}
	db := os.Getenv("DB2_DATABASE")
	if db == "" {
		db = "testdb"
	}
	user := os.Getenv("DB2_USER")
	if user == "" {
		user = "db2inst1"
	}
	pw := os.Getenv("DB2_PASSWORD")
	if pw == "" {
		pw = "password"
	}
	return fmt.Sprintf("db2://%s:%s@%s:%s/%s", user, pw, host, port, db)
}

func openConn(t *testing.T) *connectionImpl {
	t.Helper()
	drv := NewDriver(memory.DefaultAllocator)
	db, err := drv.NewDatabase(map[string]string{OptionURI: testURI(t)})
	require.NoError(t, err)
	conn, err := db.Open(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(); db.Close() })
	return conn.(*connectionImpl)
}

func readAll(t *testing.T, rr array.RecordReader) []arrow.Record {
	t.Helper()
	var out []arrow.Record
	for rr.Next() {
		rec := rr.Record()
		rec.Retain()
		out = append(out, rec)
	}
	require.NoError(t, rr.Err())
	rr.Release()
	t.Cleanup(func() {
		for _, r := range out {
			r.Release()
		}
	})
	return out
}

func TestADBCQuery(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	stmt, err := conn.NewStatement()
	require.NoError(t, err)
	defer stmt.Close()

	require.NoError(t, stmt.SetSqlQuery(`SELECT 1 AS ONE, CAST('x' AS VARCHAR(5)) AS S, CAST(1.25 AS DECIMAL(10,2)) AS D,
		DATE('2024-02-29') AS DT, TIMESTAMP('2024-02-29-13.45.59.123456') AS TS, CAST(NULL AS BIGINT) AS N
		FROM SYSIBM.SYSDUMMY1`))
	rr, _, err := stmt.ExecuteQuery(ctx)
	require.NoError(t, err)
	t.Logf("schema: %v", rr.Schema())
	recs := readAll(t, rr)
	require.Len(t, recs, 1)
	rec := recs[0]
	require.EqualValues(t, 1, rec.NumRows())
	require.Equal(t, arrow.PrimitiveTypes.Int32, rec.Schema().Field(0).Type)
	require.Equal(t, "1.25", rec.Column(2).(*array.Decimal128).ValueStr(0))
	require.Equal(t, arrow.FixedWidthTypes.Date32, rec.Schema().Field(3).Type)
	require.Equal(t, arrow.Microsecond, rec.Schema().Field(4).Type.(*arrow.TimestampType).Unit)
	require.True(t, rec.Column(5).IsNull(0))
	t.Logf("record: %v", rec)
}

func TestADBCUpdateAndStreaming(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	stmt, err := conn.NewStatement()
	require.NoError(t, err)
	defer stmt.Close()

	_ = stmt.SetSqlQuery("DROP TABLE ADBC_STREAM")
	_, _ = stmt.ExecuteUpdate(ctx)
	require.NoError(t, stmt.SetSqlQuery("CREATE TABLE ADBC_STREAM (ID INTEGER NOT NULL, V VARCHAR(30))"))
	_, err = stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = stmt.SetSqlQuery("DROP TABLE ADBC_STREAM")
		_, _ = stmt.ExecuteUpdate(ctx)
	})
	require.NoError(t, stmt.SetSqlQuery(`INSERT INTO ADBC_STREAM
		WITH T(N) AS (VALUES 1 UNION ALL SELECT N+1 FROM T WHERE N < 150000)
		SELECT N, 'value-' || CAST(N AS VARCHAR(10)) FROM T`))
	n, err := stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 150000, n)

	require.NoError(t, stmt.SetSqlQuery("SELECT ID, V FROM ADBC_STREAM ORDER BY ID"))
	rr, _, err := stmt.ExecuteQuery(ctx)
	require.NoError(t, err)
	var rows, batches int64
	for rr.Next() {
		rec := rr.Record()
		rows += rec.NumRows()
		batches++
		require.LessOrEqual(t, rec.NumRows(), int64(65536))
	}
	require.NoError(t, rr.Err())
	rr.Release()
	t.Logf("rows=%d batches=%d", rows, batches)
	require.EqualValues(t, 150000, rows)
	require.Greater(t, batches, int64(1))
}

func TestADBCMetadata(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()

	info, err := conn.GetInfo(ctx, nil)
	require.NoError(t, err)
	recs := readAll(t, info)
	require.Len(t, recs, 1)
	t.Logf("info: %v", recs[0])

	tt, err := conn.GetTableTypes(ctx)
	require.NoError(t, err)
	readAll(t, tt)

	stmt, _ := conn.NewStatement()
	defer stmt.Close()
	_ = stmt.SetSqlQuery("DROP TABLE ADBC_META")
	_, _ = stmt.ExecuteUpdate(ctx)
	require.NoError(t, stmt.SetSqlQuery("CREATE TABLE ADBC_META (ID INTEGER NOT NULL PRIMARY KEY, NAME VARCHAR(20), AMT DECIMAL(9,2))"))
	_, err = stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = stmt.SetSqlQuery("DROP TABLE ADBC_META")
		_, _ = stmt.ExecuteUpdate(ctx)
	})

	schema, err := conn.GetTableSchema(ctx, nil, nil, "ADBC_META")
	require.NoError(t, err)
	t.Logf("table schema: %v", schema)
	require.Equal(t, 3, schema.NumFields())
	require.Equal(t, "ID", schema.Field(0).Name)
	require.False(t, schema.Field(0).Nullable)
	require.Equal(t, &arrow.Decimal128Type{Precision: 9, Scale: 2}, schema.Field(2).Type)

	tbl := "ADBC_META"
	objs, err := conn.GetObjects(ctx, adbc.ObjectDepthAll, nil, nil, &tbl, nil, nil)
	require.NoError(t, err)
	orecs := readAll(t, objs)
	require.Len(t, orecs, 1)
	s := fmt.Sprint(orecs[0])
	t.Logf("objects: %s", s)
	require.Contains(t, s, "ADBC_META")
	require.Contains(t, s, "PRIMARY KEY")
	require.Contains(t, s, "DECIMAL(9,2)")
}

func TestADBCTransactions(t *testing.T) {
	conn := openConn(t)
	ctx := context.Background()
	stmt, _ := conn.NewStatement()
	defer stmt.Close()
	_ = stmt.SetSqlQuery("DROP TABLE ADBC_TX")
	_, _ = stmt.ExecuteUpdate(ctx)
	require.NoError(t, stmt.SetSqlQuery("CREATE TABLE ADBC_TX (ID INTEGER)"))
	_, err := stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.SetOption(adbc.OptionKeyAutoCommit, adbc.OptionValueEnabled)
		_ = stmt.SetSqlQuery("DROP TABLE ADBC_TX")
		_, _ = stmt.ExecuteUpdate(ctx)
	})

	require.NoError(t, conn.SetOption(adbc.OptionKeyAutoCommit, adbc.OptionValueDisabled))
	require.NoError(t, stmt.SetSqlQuery("INSERT INTO ADBC_TX VALUES (1), (2)"))
	_, err = stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.NoError(t, conn.Rollback(ctx))

	require.NoError(t, stmt.SetSqlQuery("INSERT INTO ADBC_TX VALUES (3)"))
	_, err = stmt.ExecuteUpdate(ctx)
	require.NoError(t, err)
	require.NoError(t, conn.Commit(ctx))
	require.NoError(t, conn.SetOption(adbc.OptionKeyAutoCommit, adbc.OptionValueEnabled))

	require.NoError(t, stmt.SetSqlQuery("SELECT COUNT(*) AS C FROM ADBC_TX"))
	rr, _, err := stmt.ExecuteQuery(ctx)
	require.NoError(t, err)
	recs := readAll(t, rr)
	require.Len(t, recs, 1)
	require.EqualValues(t, 1, recs[0].Column(0).(*array.Int32).Value(0))
}

func TestADBCErrorMapping(t *testing.T) {
	conn := openConn(t)
	stmt, _ := conn.NewStatement()
	defer stmt.Close()
	require.NoError(t, stmt.SetSqlQuery("SELECT * FROM NO_SUCH_TABLE_XYZ"))
	_, _, err := stmt.ExecuteQuery(context.Background())
	require.Error(t, err)
	var ae adbc.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, adbc.StatusNotFound, ae.Code)
	require.Equal(t, int32(-204), ae.VendorCode)
	require.Equal(t, "42704", string(ae.SqlState[:]))
	t.Logf("%v", err)
}
