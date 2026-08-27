package drda

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAutoBindPackage points the driver at a package that does not exist
// and expects it to be created on the fly (the Db2 for i / z/OS path).
func TestAutoBindPackage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	admin := dial(t) // default NULLID.SYSSH200 connection
	dropPkg := func() {
		_, _ = admin.ExecImmediate(ctx, "DROP PACKAGE ADBCGO.SYSSH200")
		_ = admin.Commit(ctx)
	}
	dropPkg()
	t.Cleanup(dropPkg)
	_, _, pk := fetchAll(t, admin, "SELECT PKGNAME FROM SYSCAT.PACKAGES WHERE PKGSCHEMA = 'ADBCGO'")
	require.Empty(t, pk)
	require.NoError(t, admin.Commit(ctx)) // release catalog read locks

	cfg := testConfig(t)
	cfg.PackageCollection = "ADBCGO"
	cfg.PackageID = "SYSSH200"
	c, err := Dial(ctx, cfg)
	require.NoError(t, err)
	defer c.Close()
	c.Trace = t.Logf
	_, _, rows := fetchAll(t, c, "SELECT 1 FROM SYSIBM.SYSDUMMY1")
	require.Len(t, rows, 1)
	require.True(t, c.bindAttempted, "expected the package to be bound on SQL0805N")
	require.NoError(t, c.bindError)
	_, _, pk = fetchAll(t, admin, "SELECT PKGNAME, TOTAL_SECT FROM SYSCAT.PACKAGES WHERE PKGSCHEMA = 'ADBCGO'")
	require.Len(t, pk, 1)
	require.Equal(t, int16(65), pk[0][1])
	require.NoError(t, admin.Commit(ctx))

	// Parameters use the freshly bound package too, and a second
	// connection finds the package without binding.
	ps, err := c.PrepareParams(ctx, "SELECT N FROM (VALUES 1, 2, 3) AS T(N) WHERE N > ?")
	require.NoError(t, err)
	q, err := ps.QueryParams(ctx, []Value{int32(1)})
	require.NoError(t, err)
	got, err := q.Next(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NoError(t, q.Close(ctx))
	require.NoError(t, c.Commit(ctx))

	c2, err := Dial(ctx, cfg)
	require.NoError(t, err)
	defer c2.Close()
	_, _, rows = fetchAll(t, c2, "SELECT 2 FROM SYSIBM.SYSDUMMY1")
	require.Len(t, rows, 1)
	require.False(t, c2.bindAttempted)
	require.NoError(t, c2.Commit(ctx))
	c2.Close()
	c.Close()
}
