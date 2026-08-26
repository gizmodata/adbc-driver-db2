package drda

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParamsInsertAndQuery(t *testing.T) {
	c := dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = c.ExecImmediate(ctx, "DROP TABLE ADBC_PARAMS")
	_, err := c.ExecImmediate(ctx, `CREATE TABLE ADBC_PARAMS (
		ID INTEGER NOT NULL, SI SMALLINT, BI BIGINT, DEC1 DECIMAL(12,3), R REAL, D DOUBLE, DF DECFLOAT(16),
		C CHAR(5), VC VARCHAR(100), DT DATE, TM TIME, TS TIMESTAMP(6), B BOOLEAN,
		VB VARBINARY(16), FB BINARY(4), BL BLOB(100000), CL CLOB(1000))`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.ExecImmediate(context.Background(), "DROP TABLE ADBC_PARAMS") })

	ps, err := c.PrepareParams(ctx, "INSERT INTO ADBC_PARAMS VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
	require.NoError(t, err)
	require.Len(t, ps.Params, 17)
	for i, p := range ps.Params {
		t.Logf("param %d: sqltype=%d len=%d prec=%d scale=%d", i+1, p.SQLType, p.Length, p.Precision, p.Scale)
	}
	bigBlob := bytes.Repeat([]byte{0xAB, 0xCD}, 40000) // 80 KB → EXTDTA
	rows := [][]Value{
		{int32(1), int16(7), int64(-9000000000), Decimal{big.NewInt(123456789), 3}, float32(1.5), 2.25,
			Decimal{big.NewInt(12345678), 4}, "ab", "héllo wörld グラフ", Date{2024, 2, 29}, Time{13, 45, 59},
			Timestamp{Date{2024, 2, 29}, Time{13, 45, 59}, 123456000}, true,
			[]byte{0xDE, 0xAD, 0xBE, 0xEF}, []byte{1, 2, 3, 4}, bigBlob, "clob text"},
		{int32(2), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil},
		{int32(3), int16(-1), int64(1), Decimal{big.NewInt(-5), 1}, float32(0), 0.0, Decimal{big.NewInt(1), 0},
			"xyz", "", Date{1970, 1, 1}, Time{0, 0, 0}, Timestamp{Date{2000, 1, 1}, Time{0, 0, 0}, 0}, false,
			[]byte{}, []byte{9, 9, 9, 9}, []byte{0x01}, ""},
	}
	n, err := ps.ExecBatch(ctx, rows)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	require.NoError(t, c.Commit(ctx))

	_, _, got := fetchAll(t, c, "SELECT * FROM ADBC_PARAMS ORDER BY ID")
	require.Len(t, got, 3)
	r := got[0]
	require.Equal(t, int32(1), r[0])
	require.Equal(t, int16(7), r[1])
	require.Equal(t, int64(-9000000000), r[2])
	require.Equal(t, "123456.789", r[3].(Decimal).String())
	require.Equal(t, float32(1.5), r[4])
	require.Equal(t, 2.25, r[5])
	require.Equal(t, "1234.5678", r[6].(Decimal).String())
	require.Equal(t, "ab", r[7])
	require.Equal(t, "héllo wörld グラフ", r[8])
	require.Equal(t, Date{2024, 2, 29}, r[9])
	require.Equal(t, Time{13, 45, 59}, r[10])
	require.Equal(t, Timestamp{Date{2024, 2, 29}, Time{13, 45, 59}, 123456000}, r[11])
	require.Equal(t, true, r[12])
	require.Equal(t, []byte{0xDE, 0xAD, 0xBE, 0xEF}, r[13])
	require.Equal(t, []byte{1, 2, 3, 4}, r[14])
	require.Equal(t, bigBlob, r[15])
	require.Equal(t, "clob text", r[16])
	for i := 1; i < len(got[1]); i++ {
		require.Nil(t, got[1][i], "column %d should be NULL", i)
	}
	require.Equal(t, "-0.500", got[2][3].(Decimal).String())
	require.Equal(t, false, got[2][12])

	// Parameterized query.
	qs, err := c.PrepareParams(ctx, "SELECT ID, VC FROM ADBC_PARAMS WHERE ID = ? OR VC = ?")
	require.NoError(t, err)
	require.Len(t, qs.Params, 2)
	q, err := qs.QueryParams(ctx, []Value{int32(3), "nomatch"})
	require.NoError(t, err)
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
	require.Len(t, all, 1)
	require.Equal(t, int32(3), all[0][0])
}

func TestParamsLargeBatch(t *testing.T) {
	c := dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = c.ExecImmediate(ctx, "DROP TABLE ADBC_BATCH")
	_, err := c.ExecImmediate(ctx, "CREATE TABLE ADBC_BATCH (ID INTEGER NOT NULL, V VARCHAR(50))")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.ExecImmediate(context.Background(), "DROP TABLE ADBC_BATCH") })
	ps, err := c.PrepareParams(ctx, "INSERT INTO ADBC_BATCH VALUES (?, ?)")
	require.NoError(t, err)
	const total = 20000
	const batch = 2000
	for off := 0; off < total; off += batch {
		rows := make([][]Value, 0, batch)
		for i := off; i < off+batch; i++ {
			rows = append(rows, []Value{int32(i), "value-" + itoa(i)})
		}
		n, err := ps.ExecBatch(ctx, rows)
		require.NoError(t, err)
		require.EqualValues(t, batch, n)
	}
	require.NoError(t, c.Commit(ctx))
	_, _, rows := fetchAll(t, c, "SELECT COUNT(*), SUM(ID) FROM ADBC_BATCH")
	require.Equal(t, int32(total), rows[0][0])
}

func itoa(i int) string {
	return big.NewInt(int64(i)).String()
}
