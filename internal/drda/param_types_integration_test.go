package drda

import (
	"bytes"
	"context"
	"math/big"
	"os"
	"testing"
	"time"
)

func TestParamTypesIndividually(t *testing.T) {
	if os.Getenv("DB2_HOST") == "" {
		t.Skip()
	}
	cases := []struct {
		typ string
		val Value
	}{
		{"DECFLOAT(16)", Decimal{big.NewInt(12345678), 4}},
		{"BOOLEAN", true},
		{"CHAR(5)", "ab"},
		{"BINARY(4)", []byte{1, 2, 3, 4}},
		{"VARBINARY(16)", []byte{0xDE, 0xAD}},
		{"BLOB(100000)", []byte{1, 2, 3}},
		{"BLOB(100000)", bytes.Repeat([]byte{0xAB}, 40000)},
		{"BLOB(100000)", bytes.Repeat([]byte{0xAB}, 80000)},
		{"CLOB(1000)", "clob text"},
		{"TIMESTAMP(6)", Timestamp{Date{2024, 2, 29}, Time{13, 45, 59}, 123456000}},
		{"DATE", Date{2024, 2, 29}},
		{"TIME", Time{13, 45, 59}},
		{"DECIMAL(12,3)", Decimal{big.NewInt(123456789), 3}},
		{"REAL", float32(1.5)},
	}
	for i, tc := range cases {
		tbl := "ADBC_ISO_" + itoa(i)
		func() {
			c := dial(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_, _ = c.ExecImmediate(ctx, "DROP TABLE "+tbl)
			if _, err := c.ExecImmediate(ctx, "CREATE TABLE "+tbl+" (X "+tc.typ+")"); err != nil {
				t.Logf("%-14s CREATE failed: %v", tc.typ, err)
				return
			}
			ps, err := c.PrepareParams(ctx, "INSERT INTO "+tbl+" VALUES (?)")
			if err != nil {
				t.Logf("%-14s prepare failed: %v", tc.typ, err)
				return
			}
			_, err = ps.ExecBatch(ctx, [][]Value{{tc.val}})
			if err != nil {
				t.Logf("%-14s FAIL: %v", tc.typ, err)
				return
			}
			_, _, rows := fetchAll(t, c, "SELECT X FROM "+tbl)
			t.Logf("%-14s ok -> %v", tc.typ, summarize(rows[0][0]))
			_, _ = c.ExecImmediate(ctx, "DROP TABLE "+tbl)
		}()
	}
}

func summarize(v Value) any {
	if b, ok := v.([]byte); ok && len(b) > 16 {
		return len(b)
	}
	return v
}
