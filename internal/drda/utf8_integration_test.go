package drda

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Non-Latin text in UTF-8 character columns comes back unchanged through
// every character column kind, inline and EXTDTA LOBs alike, nullable and
// NOT NULL. Mostly-ASCII values always decoded; mostly-CJK ones came back
// as mojibake (#9).
func TestUTF8NonLatinText(t *testing.T) {
	c := dial(t)
	ctx := context.Background()
	_, _ = c.ExecImmediate(ctx, "DROP TABLE ADBC_UTF8_TEXT")
	_, err := c.ExecImmediate(ctx, `CREATE TABLE ADBC_UTF8_TEXT (
		ID INTEGER NOT NULL PRIMARY KEY,
		C CHAR(40), VC VARCHAR(100), VCN VARCHAR(100) NOT NULL,
		CL CLOB(100000), CLN CLOB(100000) NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.ExecImmediate(context.Background(), "DROP TABLE ADBC_UTF8_TEXT") })

	samples := []string{"中文传媒", "你好，世界", "日本語テキスト", "한국어", "Grüße", "AB中"}
	for i, s := range samples {
		_, err := c.ExecImmediate(ctx, fmt.Sprintf(
			"INSERT INTO ADBC_UTF8_TEXT VALUES (%d, '%[2]s', '%[2]s', '%[2]s', '%[2]s', '%[2]s')", i, s))
		require.NoError(t, err, s)
	}
	require.NoError(t, c.Commit(ctx))

	_, _, rows := fetchAll(t, c, "SELECT C, VC, VCN, CL, CLN FROM ADBC_UTF8_TEXT ORDER BY ID")
	require.Len(t, rows, len(samples))
	for i, want := range samples {
		for j, got := range rows[i] {
			require.Equal(t, want, got, "row %d column %d", i, j)
		}
	}
}
