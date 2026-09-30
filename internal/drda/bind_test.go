package drda

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func pkgNotFound() *SQLCA {
	return &SQLCA{SQLCode: -805, SQLState: "51002", Message: "NULLID.SYSSH200"}
}

// A failed auto-bind must surface why, not just the SQL0805N.
func TestAutoBindReportsBindFailure(t *testing.T) {
	c := &Conn{pkgCollection: "NULLID", pkgID: "SYSSH200"}
	c.bindAttempted = true
	c.bindError = errors.New("SQLCODE=-204 SQLSTATE=42704: NULLID")

	retry, err := c.autoBind(context.Background(), pkgNotFound())
	require.False(t, retry)
	var pe *PackageError
	require.ErrorAs(t, err, &pe)
	require.ErrorContains(t, err, "SQLCODE=-805")
	require.ErrorContains(t, err, "could not create it: SQLCODE=-204")
	require.ErrorContains(t, err, "CRTLIB NULLID")
	require.ErrorContains(t, err, "adbc.db2.package")
	var ca *SQLCA
	require.ErrorAs(t, err, &ca)
	require.Equal(t, int32(-805), ca.SQLCode)
}

func TestAutoBindDisabled(t *testing.T) {
	c := &Conn{pkgCollection: "NULLID", pkgID: "SYSSH200", cfg: Config{NoAutoBind: true}}
	retry, err := c.autoBind(context.Background(), pkgNotFound())
	require.False(t, retry)
	require.ErrorContains(t, err, "auto-bind is disabled")
	require.False(t, c.bindAttempted)
}

func TestAutoBindIgnoresOtherErrors(t *testing.T) {
	c := &Conn{pkgCollection: "NULLID", pkgID: "SYSSH200"}
	other := &SQLCA{SQLCode: -805, SQLState: "51002", Message: "MYCOLL.OTHERPKG"}
	retry, err := c.autoBind(context.Background(), other)
	require.False(t, retry)
	require.Same(t, other, err)
	require.False(t, c.bindAttempted)
}
