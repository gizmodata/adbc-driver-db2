package drda

import (
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
	// correlation id) or an error reply ends a chain.
	var bindErr error
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
			if ca.IsError() && bindErr == nil {
				bindErr = fmt.Errorf("drda: binding package %s.%s failed: %w", c.pkgCollection, c.pkgID, ca)
			}
		case ddm.BGNBNDRM, ddm.PKGBNARM, ddm.PKGBPARM:
			if bindErr == nil {
				bindErr = c.replyError(d)
			}
		case ddm.RDBUPDRM, ddm.ENDUOWRM:
		default:
			if e := c.replyError(d); e != nil && bindErr == nil {
				bindErr = fmt.Errorf("drda: binding package %s.%s failed: %w", c.pkgCollection, c.pkgID, e)
			}
		}
		if !d.Chained && (d.CorrelationID >= corr || bindErr != nil) {
			break
		}
	}
	if bindErr != nil {
		return bindErr
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

// autoBind binds the package once after a SQL0805N and reports whether
// the failed operation should be retried. The caller holds c.mu.
func (c *Conn) autoBind(ctx context.Context, err error) bool {
	if !c.isOurPackageNotFound(err) || c.bindAttempted || c.cfg.NoAutoBind {
		return false
	}
	c.bindAttempted = true
	if berr := c.bindPackage(ctx); berr != nil {
		c.trace("auto-bind failed: %v", berr)
		c.bindError = berr
		return false
	}
	return true
}
