package drda

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func fetchAll(t *testing.T, c *Conn, sql string) ([]ColumnDesc, []FieldDesc, [][]Value) {
	t.Helper()
	ctx := context.Background()
	q, err := c.Query(ctx, sql)
	require.NoError(t, err, sql)
	require.True(t, q.IsResultSet(), "expected a result set for %q", sql)
	var all [][]Value
	for {
		rows, err := q.Next(ctx)
		require.NoError(t, err)
		if rows == nil {
			break
		}
		all = append(all, rows...)
	}
	require.NoError(t, q.Close(ctx))
	return q.Columns, q.Fields, all
}

func TestRichTypes(t *testing.T) {
	c := dial(t)
	ctx := context.Background()
	_, _ = c.ExecImmediate(ctx, "DROP TABLE ADBC_TYPES")
	_, err := c.ExecImmediate(ctx, `CREATE TABLE ADBC_TYPES (
		ID INTEGER NOT NULL PRIMARY KEY,
		SI SMALLINT, BI BIGINT, DEC1 DECIMAL(12,3), DEC2 DECIMAL(31,10),
		R REAL, D DOUBLE, DF DECFLOAT(16), DF34 DECFLOAT(34),
		C CHAR(5), VC VARCHAR(50), DT DATE, TM TIME, TS TIMESTAMP, TS12 TIMESTAMP(12),
		B BOOLEAN, VB VARBINARY(16), FB BINARY(4), BL BLOB(1000), CL CLOB(1000), G VARGRAPHIC(10))`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.ExecImmediate(context.Background(), "DROP TABLE ADBC_TYPES") })

	res, err := c.ExecImmediate(ctx, `INSERT INTO ADBC_TYPES VALUES
		(1, 7, 9223372036854775807, 123456789.125, -1234567890123456789.0123456789,
		 1.5, 2.25, 1234.5678, DECFLOAT('1E-20'),
		 'ab', 'héllo wörld', '2024-02-29', '13:45:59', '2024-02-29-13.45.59.123456', '2024-02-29-13.45.59.123456789012',
		 TRUE, BX'DEADBEEF', BX'01020304', BLOB(X'CAFEBABE'), 'clob text', 'グラフ'),
		(2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`)
	require.NoError(t, err)
	require.Equal(t, int64(2), res.RowsAffected)
	require.NoError(t, c.Commit(ctx))

	cols, fields, rows := fetchAll(t, c, "SELECT * FROM ADBC_TYPES ORDER BY ID")
	for i, col := range cols {
		t.Logf("col %-5s sqltype=%d len=%d prec=%d scale=%d ccsid=%d  fdoca type=0x%02X len=%d",
			col.Name, col.SQLType, col.Length, col.Precision, col.Scale, col.CCSID, fields[i].Type, fields[i].Length)
	}
	require.Len(t, rows, 2)
	r := rows[0]
	for i, v := range r {
		t.Logf("  %-5s = %T %v", cols[i].Name, v, v)
	}
	require.Equal(t, int32(1), r[0])
	require.Equal(t, int16(7), r[1])
	require.Equal(t, int64(9223372036854775807), r[2])
	require.Equal(t, "123456789.125", r[3].(Decimal).String())
	require.Equal(t, "-1234567890123456789.0123456789", r[4].(Decimal).String())
	require.Equal(t, float32(1.5), r[5])
	require.Equal(t, 2.25, r[6])
	require.Equal(t, "1234.5678", r[7].(Decimal).String())
	require.Equal(t, "0.00000000000000000001", r[8].(Decimal).String())
	require.Equal(t, "ab", r[9])
	require.Equal(t, "héllo wörld", r[10])
	require.Equal(t, Date{2024, 2, 29}, r[11])
	require.Equal(t, Time{13, 45, 59}, r[12])
	require.Equal(t, Timestamp{Date{2024, 2, 29}, Time{13, 45, 59}, 123456000}, r[13])
	require.Equal(t, Timestamp{Date{2024, 2, 29}, Time{13, 45, 59}, 123456789}, r[14])
	require.Equal(t, true, r[15])
	require.Equal(t, []byte{0xDE, 0xAD, 0xBE, 0xEF}, r[16])
	require.Equal(t, []byte{1, 2, 3, 4}, r[17])
	require.Equal(t, []byte{0xCA, 0xFE, 0xBA, 0xBE}, r[18])
	require.Equal(t, "clob text", r[19])
	require.Equal(t, "グラフ", r[20])
	for i := 1; i < len(rows[1]); i++ {
		require.Nil(t, rows[1][i], "column %s should be NULL", cols[i].Name)
	}
}

func TestMultiBlockStreaming(t *testing.T) {
	c := dial(t)
	ctx := context.Background()
	// ~200k rows × ~60 bytes ≫ 1 MiB block size, so several CNTQRY
	// round trips are needed.
	const n = 200000
	sql := fmt.Sprintf(`WITH T(N) AS (VALUES 1 UNION ALL SELECT N+1 FROM T WHERE N < %d)
		SELECT N, CAST(N * 2 AS BIGINT) AS N2, CAST('row-' || CAST(N AS VARCHAR(10)) AS VARCHAR(40)) AS S FROM T`, n)
	q, err := c.Query(ctx, sql)
	require.NoError(t, err)
	var count int64
	var batches int
	var sum int64
	for {
		rows, err := q.Next(ctx)
		require.NoError(t, err)
		if rows == nil {
			break
		}
		batches++
		for _, r := range rows {
			count++
			sum += int64(r[0].(int32))
			require.Equal(t, int64(r[0].(int32))*2, r[1])
		}
	}
	require.NoError(t, q.Close(ctx))
	t.Logf("rows=%d batches=%d", count, batches)
	require.Equal(t, int64(n), count)
	require.Equal(t, int64(n)*(n+1)/2, sum)
	require.Greater(t, batches, 1)
}

func TestSQLError(t *testing.T) {
	c := dial(t)
	_, err := c.Query(context.Background(), "SELECT * FROM NO_SUCH_TABLE_XYZ")
	require.Error(t, err)
	var ca *SQLCA
	require.ErrorAs(t, err, &ca)
	require.Equal(t, int32(-204), ca.SQLCode)
	t.Logf("error: %v", err)
	// Connection must still be usable.
	_, _, rows := fetchAll(t, c, "SELECT 1 FROM SYSIBM.SYSDUMMY1")
	require.Len(t, rows, 1)
}
