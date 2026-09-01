package db2

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

// openConnOpts is openConn with extra database options.
func openConnOpts(t *testing.T, extra map[string]string) (*connectionImpl, error) {
	t.Helper()
	opts := map[string]string{OptionURI: testURI(t)}
	for k, v := range extra {
		opts[k] = v
	}
	drv := NewDriver(memory.DefaultAllocator)
	db, err := drv.NewDatabase(opts)
	require.NoError(t, err)
	conn, err := db.Open(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	t.Cleanup(func() { conn.Close(); db.Close() })
	return conn.(*connectionImpl), nil
}

// An explicitly configured security mechanism must fail closed when the
// server refuses it — never silently downgrade (the regression fixed
// here sent the password in cleartext after an explicit request for the
// encrypted mechanism 9). The community container authenticates with
// SRVCON_AUTH=SERVER, i.e. it only accepts SECMEC 3.
func TestSecurityMechanismExplicitFailsClosed(t *testing.T) {
	conn, err := openConnOpts(t, nil)
	require.NoError(t, err)
	active, err := conn.GetOption(OptionSecurityMechanismActive)
	require.NoError(t, err)
	if active != "3" {
		t.Skipf("server negotiated SECMEC %s, not the cleartext-only setup these assertions need", active)
	}

	// Explicit encrypted mechanism against a cleartext-only server: error,
	// no downgrade.
	for _, mech := range []string{"9", "4"} {
		_, err := openConnOpts(t, map[string]string{OptionSecurityMechanism: mech})
		require.Error(t, err, "explicit secmec=%s must not silently downgrade", mech)
		require.Contains(t, err.Error(), "refusing to downgrade", "secmec=%s", mech)
	}

	// An explicit mechanism the server does accept still works and is
	// reported truthfully.
	conn3, err := openConnOpts(t, map[string]string{OptionSecurityMechanism: "3"})
	require.NoError(t, err)
	active, err = conn3.GetOption(OptionSecurityMechanismActive)
	require.NoError(t, err)
	require.Equal(t, "3", active)
}

// The batch_bytes default must be the Flight-safe 8 MiB cap, with an
// explicit 0 restoring unlimited batches.
func TestBatchBytesDefault(t *testing.T) {
	conn, err := openConnOpts(t, nil)
	require.NoError(t, err)
	got, err := conn.GetOption(OptionBatchBytes)
	require.NoError(t, err)
	require.Equal(t, "8388608", got)

	conn0, err := openConnOpts(t, map[string]string{OptionBatchBytes: "0"})
	require.NoError(t, err)
	got, err = conn0.GetOption(OptionBatchBytes)
	require.NoError(t, err)
	require.Equal(t, "0", got)
}
