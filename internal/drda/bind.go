package drda

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
)

// Package binding.
//
// Dynamic SQL over DRDA runs inside a *package* section. Db2 LUW ships
// the CLI/JCC packages (NULLID.SYSSH200 etc.) preinstalled; Db2 for i and
// Db2 for z/OS do not, and IBM's JCC driver creates them on first use with
// the DRDA bind commands. This file replays exactly what JCC's DB2Binder
// sends for SYSSH200 ("small package, WITH HOLD, 65 sections"): BGNBND,
// one BNDSQLSTT per cursor section, ENDBND.

const (
	cpPKGISOLVL ddm.CodePoint = 0x2124 // package isolation level
	cpMAXSCTNBR ddm.CodePoint = 0x2127 // maximum section number
	isolvlCS    uint16        = 0x2442 // cursor stability
	lmtblkprc   uint16        = 0x2417 // QRYBLKCTL: limited block protocol
	bindMaxSect uint16        = 65
)

// isOurPackageNotFound reports SQL0805N naming this connection's own
// dynamic-SQL package (a DROP PACKAGE of some other package yields -805
// too, and must not trigger a bind).
func (c *Conn) isOurPackageNotFound(err error) bool {
	var ca *SQLCA
	if !errors.As(err, &ca) || ca.SQLCode != -805 {
		return false
	}
	msg := strings.ToUpper(ca.Message)
	return strings.Contains(msg, strings.ToUpper(c.pkgCollection+"."+c.pkgID)) ||
		strings.Contains(msg, strings.ToUpper(c.pkgCollection+"."+c.pkgID+"."))
}

// packPKGNAMCT encodes RDBNAM(18) + RDBCOLID(18) + PKGID(18) + PKGCNSTKN(8).
func (c *Conn) packPKGNAMCT() []byte {
	pad := ddm.PadEBCDIC
	if c.ddmUTF8 {
		pad = ddm.PadASCII
	}
	b := make([]byte, 0, 62)
	b = append(b, pad(c.rdbnam, 18)...)
	b = append(b, pad(c.pkgCollection, 18)...)
	b = append(b, pad(c.pkgID, 18)...)
	b = append(b, ddm.PadASCII(c.pkgCnsTkn, 8)...) // the token is opaque bytes; JCC sends ASCII
	return ddm.Bytes(ddm.PKGNAMCT, b)
}

// bindPackage creates the dynamic-SQL package on the server. The caller
// holds c.mu.
func (c *Conn) bindPackage(ctx context.Context) error {
	c.trace("binding package %s.%s (SQL0805N)", c.pkgCollection, c.pkgID)
	var bgn []byte
	bgn = append(bgn, c.packPKGNAMCT()...)
	bgn = append(bgn, ddm.Uint16(cpPKGISOLVL, isolvlCS)...)
	bgn = append(bgn, ddm.Uint16(ddm.QRYBLKCTL, lmtblkprc)...)
	corr := uint16(1)
	c.send(ctx, ddm.NewObject(ddm.BGNBND, bgn), corr, false, false)
	corr++
	// One cursor declaration per odd section, as DB2Binder does for
	// SYSSH200; the even sections (and 65) are used for dynamic statements.
	for sect := uint16(1); sect < bindMaxSect; sect += 2 {
		stmt := fmt.Sprintf("DECLARE SQL_CURSH200C%d CURSOR WITH HOLD FOR STATEMENT%d", sect, (sect-1)*10+1)
		c.send(ctx, ddm.NewObject(ddm.BNDSQLSTT, c.packPKGNAMCSN(sect)), corr, true, false)
		c.send(ctx, c.packSQLSTT(stmt), corr, false, false)
		corr++
	}
	var end []byte
	end = append(end, c.packPKGNAMCT()...)
	end = append(end, ddm.Uint16(cpMAXSCTNBR, bindMaxSect)...)
	c.send(ctx, ddm.NewObject(ddm.ENDBND, end), corr, false, true)
	if err := c.flush(ctx); err != nil {
		return err
	}
	// The server answers every command; on the first failure it stops
	// the chain, so read DSS by DSS until either the ENDBND reply (last
	// correlation id) or an error reply ends a chain. A reply message
	// (BGNBNDRM, ...) is usually followed by an SQLCARD giving the actual
	// reason (e.g. SQL0552N), which is the error to report.
	var caErr, rmErr error
	for {
		d, err := c.readDSS(ctx)
		if err != nil {
			return err
		}
		switch d.CodePoint {
		case ddm.SQLCARD:
			ca, perr := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
			if perr != nil {
				return perr
			}
			if ca.IsError() && caErr == nil {
				caErr = ca
			}
		case ddm.RDBUPDRM, ddm.ENDUOWRM:
		default:
			if e := c.replyError(d); e != nil && rmErr == nil {
				rmErr = e
			}
		}
		if !d.Chained && (d.CorrelationID >= corr || caErr != nil || rmErr != nil) {
			break
		}
	}
	if bindErr := cmp.Or(caErr, rmErr); bindErr != nil {
		return fmt.Errorf("binding package %s.%s failed: %w", c.pkgCollection, c.pkgID, bindErr)
	}
	// The bind runs in its own unit of work; commit it.
	c.send(ctx, c.packRDBCMM(), 1, false, true)
	if err := c.flush(ctx); err != nil {
		return err
	}
	replies, err := c.readChain(ctx, 1)
	if err != nil {
		return err
	}
	if _, err := c.collectResult(replies); err != nil {
		return err
	}
	c.trace("package %s.%s bound", c.pkgCollection, c.pkgID)
	return nil
}

// PackageError reports that this connection's dynamic-SQL package does
// not exist on the server (SQL0805N) and the driver could not create it.
// It unwraps to the SQL0805N so SQLSTATE/SQLCODE are preserved.
type PackageError struct {
	Collection string
	ID         string
	Err        error // the SQL0805N
	BindErr    error // why the auto-bind failed; nil when auto-bind is disabled
}

func (e *PackageError) Error() string {
	pkg := e.Collection + "." + e.ID
	if e.BindErr == nil {
		return fmt.Sprintf("%v (package %s does not exist and auto-bind is disabled by adbc.db2.no_auto_bind)", e.Err, pkg)
	}
	return fmt.Sprintf("%v (package %s does not exist and the driver could not create it: %v; "+
		"creating it needs authority to bind packages in collection %s — on Db2 for i the %s library must exist, e.g. CRTLIB %s. "+
		"Have a DBA create it once, or set adbc.db2.package to COLLECTION.%s naming a collection you may create packages in)",
		e.Err, pkg, e.BindErr, e.Collection, e.Collection, e.Collection, e.ID)
}

func (e *PackageError) Unwrap() error { return e.Err }

// autoBind handles a failed operation's error. After a SQL0805N naming
// this connection's package it binds the package (once per connection)
// and reports that the operation should be retried; otherwise it returns
// the error to report, explaining why the package could not be created.
// The caller holds c.mu.
func (c *Conn) autoBind(ctx context.Context, err error) (retry bool, _ error) {
	if !c.isOurPackageNotFound(err) {
		return false, err
	}
	pkgErr := func(berr error) error {
		return &PackageError{Collection: c.pkgCollection, ID: c.pkgID, Err: err, BindErr: berr}
	}
	if c.cfg.NoAutoBind {
		return false, pkgErr(nil)
	}
	if c.bindAttempted {
		if c.bindError != nil {
			return false, pkgErr(c.bindError)
		}
		return false, err
	}
	c.bindAttempted = true
	if berr := c.bindPackage(ctx); berr != nil {
		c.trace("auto-bind failed: %v", berr)
		c.bindError = berr
		return false, pkgErr(berr)
	}
	return true, nil
}
