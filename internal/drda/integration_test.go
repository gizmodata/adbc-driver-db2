package drda

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testConfig reads DB2_HOST / DB2_PORT / DB2_DATABASE / DB2_USER /
// DB2_PASSWORD; tests are skipped when DB2_HOST is unset.
func testConfig(t *testing.T) Config {
	t.Helper()
	host := os.Getenv("DB2_HOST")
	if host == "" {
		t.Skip("DB2_HOST not set")
	}
	port, _ := strconv.Atoi(os.Getenv("DB2_PORT"))
	return Config{
		Host:           host,
		Port:           port,
		Database:       envOr("DB2_DATABASE", "testdb"),
		User:           envOr("DB2_USER", "db2inst1"),
		Password:       envOr("DB2_PASSWORD", "password"),
		ConnectTimeout: 10 * time.Second,
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dial(t *testing.T) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := testConfig(t)
	c, err := Dial(ctx, cfg)
	require.NoError(t, err)
	if os.Getenv("DB2_TRACE") != "" {
		c.Trace = t.Logf
		c.TraceHex = os.Getenv("DB2_TRACE") == "hex"
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestConnect(t *testing.T) {
	c := dial(t)
	t.Logf("server: %+v", c.Server)
	require.NotEmpty(t, c.Server.TypeDefName)
}

func TestSimpleQuery(t *testing.T) {
	c := dial(t)
	ctx := context.Background()
	q, err := c.Query(ctx, "SELECT 1 AS ONE, 'abc' AS S, CAST(NULL AS INTEGER) AS N FROM SYSIBM.SYSDUMMY1")
	require.NoError(t, err)
	t.Logf("columns: %+v", q.Columns)
	t.Logf("fields: %+v", q.Fields)
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
	t.Logf("rows: %+v", all)
	require.Len(t, all, 1)
	require.Equal(t, int32(1), all[0][0])
	require.Equal(t, "abc", all[0][1])
	require.Nil(t, all[0][2])
}
