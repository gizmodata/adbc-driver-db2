package drda

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
)

// Config describes how to reach a Db2 server over DRDA.
type Config struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string

	// TLS, when non-nil, wraps the TCP connection in TLS before any DRDA
	// traffic (Db2 SSL_SVR_KEYDB style; IBM Cloud Db2 uses port 50001).
	TLS *tls.Config

	// SecurityMechanism selects the SECMEC. Zero means "prefer EUSRIDPWD
	// (9), fall back to what the server offers".
	SecurityMechanism uint16

	// QueryBlockSize is the QRYBLKSZ requested for OPNQRY/CNTQRY; the
	// server returns at most this many bytes of row data per block.
	// Zero means 1 MiB (Db2 max is 10 MiB; DRDA minimum is 512).
	QueryBlockSize uint32

	ConnectTimeout time.Duration
	// ClientName is the workstation name sent in EXCSAT/SET CLIENT.
	ClientName string
	// ApplicationName is sent as EXTNAM.
	ApplicationName string
	// CurrentSchema, when set, is applied via SET CURRENT SCHEMA after
	// connecting.
	CurrentSchema string
	// PackageCollection / PackageID name the dynamic-SQL package
	// (default NULLID.SYSSH200). NoAutoBind disables creating it on
	// SQL0805N.
	PackageCollection string
	PackageID         string
	NoAutoBind        bool
}

// ServerInfo carries attributes the server reported in EXCSATRD/ACCRDBRM.
type ServerInfo struct {
	ServerClass  string // SRVCLSNM, e.g. "QDB2/LINUXX8664", "QDB2/AIX64", "QSQ" (IBM i)
	ReleaseLevel string // SRVRLSLV, e.g. "SQL11058"
	ServerName   string // SRVNAM
	ProductID    string // PRDID from ACCRDBRM, e.g. "SQL11058", "DSN13015" (z/OS), "QSQ07050" (IBM i)
	TypeDefName  string // TYPDEFNAM the server sends its data in
	LittleEndian bool
	CCSIDSBC     uint16
	CCSIDDBC     uint16
	CCSIDMBC     uint16
	SecMec       uint16
}

// Conn is one DRDA session with a Db2 server. Not safe for concurrent
// use; the ADBC layer serializes access.
type Conn struct {
	cfg  Config
	conn net.Conn
	rd   *bufio.Reader
	wr   *ddm.Writer

	Server ServerInfo

	pkgID         string
	pkgCollection string
	pkgCnsTkn     string
	bindAttempted bool
	bindError     error
	pkgSN         uint16
	rdbnam        string // 18-char padded
	qryBlkSz      uint32

	mu     sync.Mutex
	closed bool
	// ddmUTF8 is true once the server has agreed to CCSIDMGR 1208, i.e.
	// DDM-level character parameters (PKGNAMCSN, ...) are UTF-8 rather
	// than EBCDIC. Db2 LUW agrees; Db2 for i / z/OS may not.
	ddmUTF8 bool
	// openQuery tracks an OPNQRY whose ENDQRYRM has not arrived yet.
	openQuery *Query
	// Trace, when non-nil, receives one line per DSS sent/received.
	Trace func(format string, args ...any)
	// TraceHex additionally dumps reply payloads.
	TraceHex bool
}

// Package identity used for dynamic SQL. NULLID.SYSSHxyy are the CLI
// packages shipped with every Db2; section numbers within them are
// reused per statement. SYSSH200 = "small package, isolation CS, with
// hold". Section 65 is what pydrda / the C-Common client use.
const (
	defaultPkgID     = "SYSSH200"
	defaultPkgCnsTkn = "SYSLVL01"
	defaultPkgSN     = 65
	defaultQryBlkSz  = 1 << 20

	// prdid identifies us to the server as a DRDA level 11.5 client.
	prdid = "SQL11058"
	// clientTypdef: little-endian, ASCII/UTF-8 client data.
	clientTypdef = "QTDSQLX86"
)

func dialTraced(ctx context.Context, cfg Config, trace func(string, ...any)) (*Conn, error) {
	if cfg.Port == 0 {
		cfg.Port = 50000
	}
	if cfg.QueryBlockSize == 0 {
		cfg.QueryBlockSize = defaultQryBlkSz
	}
	if cfg.ClientName == "" {
		cfg.ClientName, _ = os.Hostname()
		if cfg.ClientName == "" {
			cfg.ClientName = "adbc-db2"
		}
	}
	if cfg.ApplicationName == "" {
		cfg.ApplicationName = "adbc-driver-db2"
	}
	if cfg.PackageID == "" {
		cfg.PackageID = defaultPkgID
	}
	if cfg.PackageCollection == "" {
		cfg.PackageCollection = "NULLID"
	}
	d := net.Dialer{Timeout: cfg.ConnectTimeout}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("drda: connect %s: %w", addr, err)
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
	}
	if cfg.TLS != nil {
		tcfg := cfg.TLS.Clone()
		if tcfg.ServerName == "" {
			tcfg.ServerName = cfg.Host
		}
		tconn := tls.Client(raw, tcfg)
		if err := tconn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("drda: TLS handshake with %s: %w", addr, err)
		}
		raw = tconn
	}
	c := &Conn{
		cfg:           cfg,
		conn:          raw,
		rd:            bufio.NewReaderSize(raw, 256*1024),
		wr:            ddm.NewWriter(raw),
		pkgID:         strings.ToUpper(cfg.PackageID),
		pkgCollection: strings.ToUpper(cfg.PackageCollection),
		pkgCnsTkn:     defaultPkgCnsTkn,
		pkgSN:         defaultPkgSN,
		rdbnam:        strings.ToUpper(cfg.Database),
		qryBlkSz:      cfg.QueryBlockSize,
	}
	c.Trace = trace
	if err := c.handshake(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return c, nil
}

// Dial connects and completes the DRDA handshake.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	return dialTraced(ctx, cfg, nil)
}

func (c *Conn) trace(format string, args ...any) {
	if c.Trace != nil {
		c.Trace(format, args...)
	}
}

// ---- low-level send/receive ----

func (c *Conn) send(ctx context.Context, obj ddm.Object, corr uint16, sameNext, last bool) {
	c.trace("-> %s corr=%d chained=%v (%d bytes)", obj.CodePoint(), corr, !last, len(obj))
	c.wr.WriteRequest(obj, corr, sameNext, last)
}

func (c *Conn) flush(ctx context.Context) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(dl)
	} else {
		_ = c.conn.SetWriteDeadline(time.Time{})
	}
	return c.wr.Flush()
}

func (c *Conn) readDSS(ctx context.Context) (*ddm.DSS, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetReadDeadline(dl)
	} else {
		_ = c.conn.SetReadDeadline(time.Time{})
	}
	d, err := ddm.ReadDSS(c.rd)
	if err != nil {
		return nil, err
	}
	c.trace("<- %s corr=%d chained=%v (%d bytes)", d.CodePoint, d.CorrelationID, d.Chained, len(d.Payload))
	if c.TraceHex && len(d.Payload) <= 4096 {
		c.trace("   % X", d.Payload)
	}
	return d, nil
}

// readChain reads reply DSSs until the chain ends (a DSS without the
// chained flag) AND the reply correlates to lastCorr. Db2 occasionally
// ends a chain early (e.g. after the SQLDARD for a PRPSQLSTT) and then
// continues with the replies for the next command in the same
// transmission, so "unchained" alone is not the end of the transmission.
func (c *Conn) readChain(ctx context.Context, lastCorr uint16) ([]*ddm.DSS, error) {
	var out []*ddm.DSS
	sawError := false
	for {
		d, err := c.readDSS(ctx)
		if err != nil {
			return out, err
		}
		out = append(out, d)
		if c.isErrorReply(d) {
			sawError = true
		}
		if d.Chained {
			continue
		}
		// End of a chain. We are done when the reply belongs to the last
		// request we sent — or when a command failed, in which case the
		// server discards the remaining chained requests and sends
		// nothing more for them.
		if d.CorrelationID >= lastCorr || sawError {
			return out, nil
		}
	}
}

// isErrorReply reports whether a reply DSS signals a failed command
// (an SQLCARD with a negative SQLCODE, or an error reply message).
func (c *Conn) isErrorReply(d *ddm.DSS) bool {
	switch d.CodePoint {
	case ddm.SQLCARD:
		if len(d.Payload) > 0 && d.Payload[0] == 0xFF {
			return false
		}
		ca, err := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
		return err == nil && ca.IsError()
	case ddm.SQLDARD:
		ca, _, err := ParseSQLDARD(d.Payload, c.Server.LittleEndian)
		return err == nil && ca.IsError()
	case ddm.SQLERRRM, ddm.BGNBNDRM, ddm.PKGBNARM, ddm.PKGBPARM, ddm.CMDCHKRM, ddm.SYNTAXRM,
		ddm.PRCCNVRM, ddm.CMDNSPRM, ddm.OBJNSPRM, ddm.PRMNSPRM, ddm.VALNSPRM, ddm.RDBNFNRM,
		ddm.RDBATHRM, ddm.RDBNACRM, ddm.RDBAFLRM, ddm.ABNUOWRM, ddm.QRYNOPRM, ddm.DTAMCHRM,
		ddm.OPNQFLRM, ddm.AGNPRMRM, ddm.RSCLMTRM, ddm.MGRLVLRM, ddm.CMDATHRM:
		return true
	}
	return false
}

// ---- handshake ----

func (c *Conn) handshake(ctx context.Context) error {
	kp, err := newDHKeyPair()
	if err != nil {
		return err
	}
	secmec := c.cfg.SecurityMechanism
	if secmec == 0 {
		secmec = ddm.SecMecEUSRIDPWD
	}
	if c.cfg.TLS != nil && c.cfg.SecurityMechanism == 0 {
		// Over TLS a cleartext-in-tunnel password is fine and avoids the
		// DES round trip; Db2 on Cloud in particular only offers 3.
		secmec = ddm.SecMecUSRIDPWD
	}

	// EXCSAT + ACCSEC in one transmission.
	c.send(ctx, c.packEXCSAT(), 1, false, false)
	c.send(ctx, c.packACCSEC(secmec, kp), 2, false, true)
	if err := c.flush(ctx); err != nil {
		return fmt.Errorf("drda: send EXCSAT/ACCSEC: %w", err)
	}
	replies, err := c.readChain(ctx, 2)
	if err != nil {
		return fmt.Errorf("drda: read EXCSATRD/ACCSECRD: %w", err)
	}
	var serverToken []byte
	serverSecMec := secmec
	for _, d := range replies {
		switch d.CodePoint {
		case ddm.EXCSATRD:
			c.parseEXCSATRD(d.Payload)
		case ddm.ACCSECRD:
			p, err := ddm.ParseParams(d.Payload)
			if err != nil {
				return err
			}
			serverToken = p.Map[ddm.SECTKN]
			offered := offeredSecMecs(p)
			if cd, ok := p.Map[ddm.SECCHKCD]; ok && len(cd) > 0 && cd[0] != 0 {
				if cd[0] == 0x01 && len(offered) > 0 {
					// Not supported; the server lists what it accepts.
					serverSecMec = pickSecMec(offered)
					c.trace("server offers SECMEC %v", offered)
				} else {
					return fmt.Errorf("drda: ACCSEC rejected (SECCHKCD=0x%02X): %s", cd[0], secChkCodeText(cd[0]))
				}
			} else if len(offered) > 0 {
				serverSecMec = offered[0]
			}
		case ddm.RDBNFNRM:
			return fmt.Errorf("drda: database %q not found on server", c.cfg.Database)
		default:
			if err := c.replyError(d); err != nil {
				return err
			}
		}
	}
	// If the server countered with a different mechanism, re-issue ACCSEC.
	if serverSecMec != secmec {
		c.trace("server requires SECMEC %d (we offered %d); renegotiating", serverSecMec, secmec)
		secmec = serverSecMec
		c.send(ctx, c.packACCSEC(secmec, kp), 1, false, true)
		if err := c.flush(ctx); err != nil {
			return err
		}
		replies, err = c.readChain(ctx, 1)
		if err != nil {
			return err
		}
		for _, d := range replies {
			if d.CodePoint == ddm.ACCSECRD {
				p, err := ddm.ParseParams(d.Payload)
				if err != nil {
					return err
				}
				serverToken = p.Map[ddm.SECTKN]
				if cd, ok := p.Map[ddm.SECCHKCD]; ok && len(cd) > 0 && cd[0] != 0 {
					return fmt.Errorf("drda: ACCSEC rejected (SECCHKCD=0x%02X): %s", cd[0], secChkCodeText(cd[0]))
				}
			} else if err := c.replyError(d); err != nil {
				return err
			}
		}
	}
	c.Server.SecMec = secmec

	secchk, err := c.packSECCHK(secmec, serverToken, kp)
	if err != nil {
		return err
	}
	c.send(ctx, secchk, 1, false, false)
	c.send(ctx, c.packACCRDB(), 2, false, true)
	if err := c.flush(ctx); err != nil {
		return fmt.Errorf("drda: send SECCHK/ACCRDB: %w", err)
	}
	replies, err = c.readChain(ctx, 2)
	if err != nil {
		return fmt.Errorf("drda: read SECCHKRM/ACCRDBRM: %w", err)
	}
	for _, d := range replies {
		switch d.CodePoint {
		case ddm.SECCHKRM:
			p, err := ddm.ParseParams(d.Payload)
			if err != nil {
				return err
			}
			if cd, ok := p.Map[ddm.SECCHKCD]; ok && len(cd) > 0 && cd[0] != 0 {
				return &AuthError{Code: cd[0], Message: secChkCodeText(cd[0])}
			}
		case ddm.ACCRDBRM:
			if err := c.parseACCRDBRM(d.Payload); err != nil {
				return err
			}
		case ddm.SQLCARD:
			ca, err := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
			if err != nil {
				return err
			}
			if ca.IsError() {
				return ca
			}
		default:
			if err := c.replyError(d); err != nil {
				return err
			}
		}
	}
	if c.Server.TypeDefName == "" {
		return errors.New("drda: server did not send ACCRDBRM")
	}
	return c.postConnect(ctx)
}

// postConnect performs the session setup Db2 clients customarily do:
// declare CCSIDMGR 1208 (UTF-8 everywhere), set the workstation name,
// and optionally the current schema.
func (c *Conn) postConnect(ctx context.Context) error {
	// EXCSAT with CCSIDMGR 1208: from here on DDM-level character
	// parameters (PKGNAMCSN, RDBNAM in commands, ...) are UTF-8 rather
	// than EBCDIC. Sent alone (it has its own reply chain).
	ccsidMgr := uint16(ddm.CCSIDMGR)
	c.send(ctx, ddm.NewObject(ddm.EXCSAT, ddm.Bytes(ddm.MGRLVLLS, []byte{
		byte(ccsidMgr >> 8), byte(ccsidMgr), byte(1208 >> 8), byte(1208 & 0xFF),
	})), 1, false, true)
	if err := c.flush(ctx); err != nil {
		return err
	}
	replies, err := c.readChain(ctx, 1)
	if err != nil {
		return fmt.Errorf("drda: CCSIDMGR EXCSAT: %w", err)
	}
	for _, d := range replies {
		if d.CodePoint == ddm.EXCSATRD {
			c.ddmUTF8 = excsatrdAgreesCCSID1208(d.Payload)
			if !c.ddmUTF8 {
				c.trace("server did not accept CCSIDMGR 1208; DDM character parameters stay EBCDIC")
			}
			continue
		}
		if err := c.replyError(d); err != nil {
			return err
		}
	}
	var stmts []string
	for _, s := range stmts {
		// Best-effort: SET CLIENT is Db2 LUW-specific; other platforms
		// reject it and that's fine.
		if _, err := c.ExecImmediate(ctx, s); err != nil {
			var ca *SQLCA
			if errors.As(err, &ca) {
				c.trace("post-connect %q ignored: %v", s, err)
				continue
			}
			return err
		}
	}
	return nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (c *Conn) parseEXCSATRD(body []byte) {
	p, err := ddm.ParseParams(body)
	if err != nil {
		return
	}
	c.Server.ServerClass, _ = p.EBCDICString(ddm.SRVCLSNM)
	c.Server.ReleaseLevel, _ = p.EBCDICString(ddm.SRVRLSLV)
	c.Server.ServerName, _ = p.EBCDICString(ddm.SRVNAM)
	c.Server.ServerClass = strings.TrimSpace(c.Server.ServerClass)
	c.Server.ReleaseLevel = strings.TrimSpace(c.Server.ReleaseLevel)
	c.Server.ServerName = strings.TrimSpace(c.Server.ServerName)
}

func (c *Conn) parseACCRDBRM(body []byte) error {
	p, err := ddm.ParseParams(body)
	if err != nil {
		return err
	}
	c.Server.ProductID, _ = p.EBCDICString(ddm.PRDID)
	c.Server.ProductID = strings.TrimSpace(c.Server.ProductID)
	td, _ := p.EBCDICString(ddm.TYPDEFNAM)
	td = strings.TrimSpace(td)
	c.Server.TypeDefName = td
	switch td {
	case "QTDSQLX86":
		c.Server.LittleEndian = true
		c.Server.CCSIDSBC, c.Server.CCSIDDBC, c.Server.CCSIDMBC = 1252, 1200, 1208
	case "QTDSQLASC", "QTDSQLJVM":
		c.Server.LittleEndian = false
		c.Server.CCSIDSBC, c.Server.CCSIDDBC, c.Server.CCSIDMBC = 819, 1200, 1208
	case "QTDSQL370", "QTDSQL400":
		c.Server.LittleEndian = false
		c.Server.CCSIDSBC, c.Server.CCSIDDBC, c.Server.CCSIDMBC = 500, 1200, 1208
	default:
		c.Server.LittleEndian = false
	}
	if ov, ok := p.Map[ddm.TYPDEFOVR]; ok {
		op, err := ddm.ParseParams(ov)
		if err == nil {
			if v, ok := op.Uint16(ddm.CCSIDSBC); ok {
				c.Server.CCSIDSBC = v
			}
			if v, ok := op.Uint16(ddm.CCSIDDBC); ok {
				c.Server.CCSIDDBC = v
			}
			if v, ok := op.Uint16(ddm.CCSIDMBC); ok {
				c.Server.CCSIDMBC = v
			}
		}
	}
	return nil
}

// ---- request packers ----

func (c *Conn) packEXCSAT() ddm.Object {
	mgrs := []uint16{
		uint16(ddm.AGENT), 10,
		uint16(ddm.SQLAM), 11,
		uint16(ddm.CMNTCPIP), 5,
		uint16(ddm.RDB), 12,
		uint16(ddm.SECMGR), 9,
		uint16(ddm.UNICODEMGR), 1208,
	}
	mgrlvlls := make([]byte, 0, 2*len(mgrs))
	for _, v := range mgrs {
		mgrlvlls = append(mgrlvlls, byte(v>>8), byte(v))
	}
	var body []byte
	body = append(body, ddm.EBCDIC(ddm.EXTNAM, c.cfg.ApplicationName)...)
	body = append(body, ddm.EBCDIC(ddm.SRVNAM, c.cfg.ClientName)...)
	body = append(body, ddm.EBCDIC(ddm.SRVRLSLV, prdid)...)
	body = append(body, ddm.Bytes(ddm.MGRLVLLS, mgrlvlls)...)
	body = append(body, ddm.EBCDIC(ddm.SRVCLSNM, "QDB2/JVM")...)
	return ddm.NewObject(ddm.EXCSAT, body)
}

func (c *Conn) rdbnamEBCDIC() []byte { return ddm.PadEBCDIC(c.rdbnam, 18) }

func (c *Conn) packACCSEC(secmec uint16, kp *dhKeyPair) ddm.Object {
	var body []byte
	body = append(body, ddm.Uint16(ddm.SECMEC, secmec)...)
	body = append(body, ddm.Bytes(ddm.RDBNAM, c.rdbnamEBCDIC())...)
	if secmec == ddm.SecMecEUSRIDPWD {
		body = append(body, ddm.Bytes(ddm.SECTKN, kp.public)...)
	}
	return ddm.NewObject(ddm.ACCSEC, body)
}

func (c *Conn) packSECCHK(secmec uint16, serverToken []byte, kp *dhKeyPair) (ddm.Object, error) {
	var body []byte
	body = append(body, ddm.Uint16(ddm.SECMEC, secmec)...)
	body = append(body, ddm.Bytes(ddm.RDBNAM, c.rdbnamEBCDIC())...)
	switch secmec {
	case ddm.SecMecEUSRIDPWD:
		enc, err := kp.encryptor(serverToken)
		if err != nil {
			return nil, err
		}
		u, err := enc(ddm.EncodeEBCDIC(c.cfg.User))
		if err != nil {
			return nil, err
		}
		pw, err := enc(ddm.EncodeEBCDIC(c.cfg.Password))
		if err != nil {
			return nil, err
		}
		body = append(body, ddm.Bytes(ddm.SECTKN, u)...)
		body = append(body, ddm.Bytes(ddm.SECTKN, pw)...)
	case ddm.SecMecUSRIDPWD:
		body = append(body, ddm.EBCDIC(ddm.USRID, c.cfg.User)...)
		body = append(body, ddm.EBCDIC(ddm.PASSWORD, c.cfg.Password)...)
	case ddm.SecMecUSRIDONL:
		body = append(body, ddm.EBCDIC(ddm.USRID, c.cfg.User)...)
	default:
		return nil, fmt.Errorf("drda: security mechanism %d not supported by this driver (supported: 3, 4, 9)", secmec)
	}
	return ddm.NewObject(ddm.SECCHK, body), nil
}

func (c *Conn) packACCRDB() ddm.Object {
	var body []byte
	body = append(body, ddm.Bytes(ddm.RDBNAM, c.rdbnamEBCDIC())...)
	body = append(body, ddm.Uint16(ddm.RDBACCCL, uint16(ddm.SQLAM))...)
	body = append(body, ddm.EBCDIC(ddm.PRDID, prdid)...)
	body = append(body, ddm.EBCDIC(ddm.TYPDEFNAM, clientTypdef)...)
	// CRRTKN: correlation token — 19 bytes; content is free-form.
	crrtkn := make([]byte, 0, 19)
	crrtkn = append(crrtkn, ddm.PadEBCDIC("ADBCGO", 8)...)
	crrtkn = append(crrtkn, '.')
	crrtkn = append(crrtkn, ddm.PadEBCDIC("GOLANG", 6)...)
	crrtkn = append(crrtkn, 0x01, 0x55, 0x63, 0x0D)
	body = append(body, ddm.Bytes(ddm.CRRTKN, crrtkn)...)
	// TYPDEFOVR: client CCSIDs — SBC 1208 (UTF-8), DBC 1200 (UTF-16BE),
	// MBC 1208 (UTF-8). Db2 then sends all character data as UTF-8.
	var ov []byte
	ov = append(ov, ddm.Uint16(ddm.CCSIDSBC, 1208)...)
	ov = append(ov, ddm.Uint16(ddm.CCSIDDBC, 1200)...)
	ov = append(ov, ddm.Uint16(ddm.CCSIDMBC, 1208)...)
	body = append(body, ddm.Bytes(ddm.TYPDEFOVR, ov)...)
	return ddm.NewObject(ddm.ACCRDB, body)
}

// packPKGNAMCSN encodes RDBNAM(18) + RDBCOLID(18) + PKGID(18) + PKGCNSTKN(8) + PKGSN(2)
// in the DDM character encoding negotiated with the server.
func (c *Conn) packPKGNAMCSN(section uint16) []byte {
	pad := ddm.PadEBCDIC
	if c.ddmUTF8 {
		pad = ddm.PadASCII
	}
	b := make([]byte, 0, 64)
	b = append(b, pad(c.rdbnam, 18)...)
	b = append(b, pad(c.pkgCollection, 18)...)
	b = append(b, pad(c.pkgID, 18)...)
	b = append(b, ddm.PadASCII(c.pkgCnsTkn, 8)...) // opaque token; JCC sends it as ASCII bytes
	b = append(b, byte(section>>8), byte(section))
	return ddm.Bytes(ddm.PKGNAMCSN, b)
}

// excsatrdAgreesCCSID1208 reports whether an EXCSATRD's MGRLVLLS lists
// CCSIDMGR at level 1208.
func excsatrdAgreesCCSID1208(body []byte) bool {
	p, err := ddm.ParseParams(body)
	if err != nil {
		return false
	}
	ls, ok := p.Map[ddm.MGRLVLLS]
	if !ok {
		return false
	}
	for i := 0; i+4 <= len(ls); i += 4 {
		mgr := ddm.CodePoint(uint16(ls[i])<<8 | uint16(ls[i+1]))
		lvl := uint16(ls[i+2])<<8 | uint16(ls[i+3])
		if mgr == ddm.CCSIDMGR && lvl == 1208 {
			return true
		}
	}
	return false
}

func (c *Conn) packSQLSTT(sql string) ddm.Object {
	// SQLSTT: nullable VCM/VCS pair; we send mixed (UTF-8) form.
	b := make([]byte, 0, len(sql)+6)
	b = append(b, 0x00, byte(len(sql)>>24), byte(len(sql)>>16), byte(len(sql)>>8), byte(len(sql)))
	b = append(b, sql...)
	b = append(b, 0xFF)
	return ddm.NewObject(ddm.SQLSTT, b)
}

func (c *Conn) packSQLATTR(attr string) ddm.Object {
	b := make([]byte, 0, len(attr)+6)
	b = append(b, 0x00, byte(len(attr)>>24), byte(len(attr)>>16), byte(len(attr)>>8), byte(len(attr)))
	b = append(b, attr...)
	b = append(b, 0xFF)
	return ddm.NewObject(ddm.SQLATTR, b)
}

func (c *Conn) packEXCSQLIMM(section uint16) ddm.Object {
	var body []byte
	body = append(body, c.packPKGNAMCSN(section)...)
	body = append(body, ddm.Byte(ddm.RDBCMTOK, 0xF1)...)
	return ddm.NewObject(ddm.EXCSQLIMM, body)
}

func (c *Conn) packPRPSQLSTT(section uint16) ddm.Object {
	var body []byte
	body = append(body, c.packPKGNAMCSN(section)...)
	body = append(body, ddm.Byte(ddm.RTNSQLDA, 0xF1)...)
	return ddm.NewObject(ddm.PRPSQLSTT, body)
}

func (c *Conn) packDSCSQLSTT(section uint16) ddm.Object {
	var body []byte
	body = append(body, c.packPKGNAMCSN(section)...)
	body = append(body, ddm.Byte(ddm.TYPSQLDA, 0x01)...) // light output SQLDA
	return ddm.NewObject(ddm.DSCSQLSTT, body)
}

func (c *Conn) packEXCSQLSTT(section uint16) ddm.Object {
	var body []byte
	body = append(body, c.packPKGNAMCSN(section)...)
	body = append(body, ddm.Byte(ddm.RDBCMTOK, 0xF1)...)
	return ddm.NewObject(ddm.EXCSQLSTT, body)
}

func (c *Conn) packOPNQRY(section uint16, withParams bool) ddm.Object {
	var body []byte
	body = append(body, c.packPKGNAMCSN(section)...)
	body = append(body, ddm.Uint32(ddm.QRYBLKSZ, c.qryBlkSz)...)
	body = append(body, ddm.Uint16(ddm.MAXBLKEXT, 0xFFFF)...) // unlimited extra blocks per reply... within QRYBLKSZ
	body = append(body, ddm.Byte(ddm.QRYCLSIMP, 0x01)...)     // close implicitly at end of data
	if withParams {
		body = append(body, ddm.Byte(ddm.DYNDTAFMT, 0xF1)...)
	}
	return ddm.NewObject(ddm.OPNQRY, body)
}

func (c *Conn) packCNTQRY(section uint16, qryinsid uint64) ddm.Object {
	var body []byte
	body = append(body, c.packPKGNAMCSN(section)...)
	body = append(body, ddm.Uint32(ddm.QRYBLKSZ, c.qryBlkSz)...)
	body = append(body, ddm.Uint64(ddm.QRYINSID, qryinsid)...)
	body = append(body, ddm.Byte(ddm.RTNEXTDTA, 0x02)...) // return all EXTDTA with the block
	return ddm.NewObject(ddm.CNTQRY, body)
}

func (c *Conn) packCLSQRY(section uint16, qryinsid uint64) ddm.Object {
	var body []byte
	body = append(body, c.packPKGNAMCSN(section)...)
	body = append(body, ddm.Uint64(ddm.QRYINSID, qryinsid)...)
	return ddm.NewObject(ddm.CLSQRY, body)
}

func (c *Conn) packRDBCMM() ddm.Object    { return ddm.NewObject(ddm.RDBCMM, nil) }
func (c *Conn) packRDBRLLBCK() ddm.Object { return ddm.NewObject(ddm.RDBRLLBCK, nil) }

// ---- reply handling ----

// AuthError is a SECCHK failure.
type AuthError struct {
	Code    byte
	Message string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("drda: authentication failed (SECCHKCD=0x%02X): %s", e.Code, e.Message)
}

func secChkCodeText(cd byte) string {
	switch cd {
	case 0x00:
		return "ok"
	case 0x01:
		return "security mechanism not supported"
	case 0x02:
		return "DCE informational status"
	case 0x03:
		return "DCE retryable error"
	case 0x04:
		return "DCE non-retryable error"
	case 0x05:
		return "GSSAPI informational status"
	case 0x06:
		return "GSSAPI retryable error"
	case 0x07:
		return "GSSAPI non-retryable error"
	case 0x08:
		return "local security service info"
	case 0x09:
		return "local security service retryable error"
	case 0x0A:
		return "local security service non-retryable error"
	case 0x0B:
		return "SECTKN missing or invalid"
	case 0x0E:
		return "password expired"
	case 0x0F:
		return "password invalid"
	case 0x10:
		return "password missing"
	case 0x12:
		return "user id missing"
	case 0x13:
		return "user id invalid"
	case 0x14:
		return "user id revoked"
	case 0x15:
		return "new password invalid"
	case 0x16:
		return "authentication failed due to connectivity restrictions"
	case 0x17:
		return "invalid GSS-API server credential"
	case 0x18:
		return "GSS-API server credential expired"
	case 0x19:
		return "continue: additional tokens required"
	case 0x1A:
		return "switch user: SECMEC not supported"
	case 0x1B:
		return "switch user: encryption not supported"
	case 0x1C:
		return "user id not allowed"
	default:
		return "unknown"
	}
}

// ProtocolError is a DDM reply message signalling a command-level
// failure (not an SQL error).
type ProtocolError struct {
	CodePoint ddm.CodePoint
	Severity  uint16
	Detail    string
}

func (e *ProtocolError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("drda: %s (SVRCOD=%d): %s", e.CodePoint, e.Severity, e.Detail)
	}
	return fmt.Sprintf("drda: %s (SVRCOD=%d)", e.CodePoint, e.Severity)
}

// replyError converts an unexpected reply message into an error. Data
// objects and informational replies return nil.
func (c *Conn) replyError(d *ddm.DSS) error {
	switch d.CodePoint {
	case ddm.SQLCARD, ddm.SQLDARD, ddm.QRYDSC, ddm.QRYDTA, ddm.EXTDTA, ddm.SQLDTARD,
		ddm.OPNQRYRM, ddm.ENDQRYRM, ddm.ENDUOWRM, ddm.EXCSATRD, ddm.ACCSECRD,
		ddm.SECCHKRM, ddm.ACCRDBRM, ddm.SQLRSLRD, ddm.SQLCINRD, ddm.CMDCMPRM, ddm.RDBUPDRM:
		return nil
	}
	p, perr := ddm.ParseParams(d.Payload)
	pe := &ProtocolError{CodePoint: d.CodePoint}
	if perr == nil {
		pe.Severity, _ = p.Uint16(ddm.SVRCOD)
		if s, ok := p.EBCDICString(ddm.SRVDGN); ok {
			pe.Detail = strings.TrimSpace(s)
		}
		switch d.CodePoint {
		case ddm.RDBNFNRM:
			pe.Detail = fmt.Sprintf("database %q not found", c.cfg.Database)
		case ddm.RDBATHRM:
			pe.Detail = "not authorized to database"
		case ddm.RDBAFLRM:
			pe.Detail = "database access failed"
		case ddm.CMDCHKRM:
			pe.Detail = "command check (malformed request)"
		case ddm.SYNTAXRM:
			if cd, ok := p.Map[ddm.SYNERRCD]; ok && len(cd) > 0 {
				pe.Detail = fmt.Sprintf("data stream syntax error code 0x%02X", cd[0])
			}
			if cp, ok := p.Uint16(ddm.CODPNT); ok {
				pe.Detail += fmt.Sprintf(" at code point %s", ddm.CodePoint(cp))
			}
		case ddm.MGRLVLRM:
			pe.Detail = "manager-level conflict"
		case ddm.PRCCNVRM:
			if cd, ok := p.Map[ddm.PRCCNVCD]; ok && len(cd) > 0 {
				pe.Detail = fmt.Sprintf("conversational protocol error code 0x%02X", cd[0])
			}
		case ddm.CMDNSPRM, ddm.OBJNSPRM, ddm.PRMNSPRM, ddm.VALNSPRM:
			if cp, ok := p.Uint16(ddm.CODPNT); ok {
				pe.Detail = fmt.Sprintf("not supported: %s", ddm.CodePoint(cp))
			}
		case ddm.ABNUOWRM:
			pe.Detail = "unit of work abnormally ended (rolled back)"
		case ddm.QRYNOPRM:
			pe.Detail = "query not open"
		case ddm.QRYPOPRM:
			pe.Detail = "query previously opened"
		case ddm.DTAMCHRM:
			pe.Detail = "data descriptor mismatch"
		case ddm.OPNQFLRM:
			pe.Detail = "open query failure"
		case ddm.BGNBNDRM:
			pe.Detail = "begin bind error"
			if cd, ok := p.Map[ddm.RSNCOD]; ok && len(cd) > 0 {
				pe.Detail = fmt.Sprintf("begin bind error (reason 0x%02X)", cd[0])
			}
		case ddm.PKGBNARM:
			pe.Detail = "package bind not active"
		case ddm.PKGBPARM:
			pe.Detail = "package bind process active"
		}
	}
	return pe
}

// ---- statements ----

// Result summarizes a non-query statement.
type Result struct {
	RowsAffected int64
	SQLCA        *SQLCA
}

// ExecImmediate runs a statement with no parameters and no result set
// (DDL, DML, SET ...). Autocommit is the caller's responsibility.
func (c *Conn) ExecImmediate(ctx context.Context, sql string) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.execImmediateLocked(ctx, sql)
	if err != nil && c.autoBind(ctx, err) {
		res, err = c.execImmediateLocked(ctx, sql)
	}
	return res, err
}

func (c *Conn) execImmediateLocked(ctx context.Context, sql string) (*Result, error) {
	if err := c.ensureNoOpenQuery(ctx); err != nil {
		return nil, err
	}
	c.trace("sql (immediate): %s", sql)
	c.send(ctx, c.packEXCSQLIMM(c.pkgSN), 1, true, false)
	c.send(ctx, c.packSQLSTT(sql), 1, false, true)
	if err := c.flush(ctx); err != nil {
		return nil, err
	}
	replies, err := c.readChain(ctx, 1)
	if err != nil {
		return nil, err
	}
	return c.collectResult(replies)
}

func (c *Conn) collectResult(replies []*ddm.DSS) (*Result, error) {
	res := &Result{RowsAffected: -1}
	var firstErr error
	for _, d := range replies {
		switch d.CodePoint {
		case ddm.SQLCARD:
			ca, err := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
			if err != nil {
				return nil, err
			}
			if ca == nil {
				continue
			}
			if ca.IsError() && firstErr == nil {
				firstErr = ca
			}
			if res.SQLCA == nil || ca.IsError() {
				res.SQLCA = ca
			}
			if !ca.IsError() {
				res.RowsAffected = ca.RowCount()
			}
		case ddm.SQLERRRM:
			// Followed by an SQLCARD with the details.
		case ddm.ENDUOWRM:
		default:
			if err := c.replyError(d); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return res, nil
}

// Commit ends the unit of work with RDBCMM.
func (c *Conn) Commit(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureNoOpenQuery(ctx); err != nil {
		return err
	}
	c.send(ctx, c.packRDBCMM(), 1, false, true)
	if err := c.flush(ctx); err != nil {
		return err
	}
	replies, err := c.readChain(ctx, 1)
	if err != nil {
		return err
	}
	_, err = c.collectResult(replies)
	return err
}

// Rollback ends the unit of work with RDBRLLBCK.
func (c *Conn) Rollback(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureNoOpenQuery(ctx); err != nil {
		return err
	}
	c.send(ctx, c.packRDBRLLBCK(), 1, false, true)
	if err := c.flush(ctx); err != nil {
		return err
	}
	replies, err := c.readChain(ctx, 1)
	if err != nil {
		return err
	}
	_, err = c.collectResult(replies)
	return err
}

// Close commits nothing; it releases the socket. Callers wanting a
// clean unit-of-work end should Commit or Rollback first.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// Describe prepares sql and returns the result-column and parameter
// descriptions without opening a cursor.
func (c *Conn) Describe(ctx context.Context, sql string) (cols, params []ColumnDesc, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cols, params, err = c.describeLocked(ctx, sql)
	if err != nil && c.autoBind(ctx, err) {
		cols, params, err = c.describeLocked(ctx, sql)
	}
	return cols, params, err
}

func (c *Conn) describeLocked(ctx context.Context, sql string) (cols, params []ColumnDesc, err error) {
	if err := c.ensureNoOpenQuery(ctx); err != nil {
		return nil, nil, err
	}
	c.trace("sql (describe): %s", sql)
	c.send(ctx, c.packPRPSQLSTT(c.pkgSN), 1, true, false)
	c.send(ctx, c.packSQLSTT(sql), 1, false, false)
	c.send(ctx, c.packDSCSQLSTT(c.pkgSN), 2, false, true)
	if err := c.flush(ctx); err != nil {
		return nil, nil, err
	}
	replies, err := c.readChain(ctx, 2)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range replies {
		switch d.CodePoint {
		case ddm.SQLDARD:
			ca, desc, perr := ParseSQLDARD(d.Payload, c.Server.LittleEndian)
			if perr != nil {
				return nil, nil, perr
			}
			if ca.IsError() {
				return nil, nil, ca
			}
			if d.CorrelationID == 1 && cols == nil {
				cols = desc
			} else {
				params = desc
			}
		case ddm.SQLCARD:
			ca, perr := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
			if perr != nil {
				return nil, nil, perr
			}
			if ca.IsError() {
				return nil, nil, ca
			}
		default:
			if e := c.replyError(d); e != nil {
				return nil, nil, e
			}
		}
	}
	return cols, params, nil
}

func (c *Conn) ensureNoOpenQuery(ctx context.Context) error {
	if c.openQuery != nil && !c.openQuery.done {
		return c.openQuery.closeLocked(ctx)
	}
	c.openQuery = nil
	return nil
}

// offeredSecMecs lists every SECMEC value in an ACCSECRD, in order.
func offeredSecMecs(p *ddm.Params) []uint16 {
	var out []uint16
	for _, kv := range p.All {
		if kv.CodePoint == ddm.SECMEC && len(kv.Value) >= 2 {
			out = append(out, uint16(kv.Value[0])<<8|uint16(kv.Value[1]))
		}
	}
	return out
}

// pickSecMec chooses the strongest mechanism we implement from the
// server's list: 9 (encrypted) > 3 (cleartext) > 4 (user id only).
func pickSecMec(offered []uint16) uint16 {
	for _, want := range []uint16{ddm.SecMecEUSRIDPWD, ddm.SecMecUSRIDPWD, ddm.SecMecUSRIDONL} {
		for _, o := range offered {
			if o == want {
				return o
			}
		}
	}
	return offered[0]
}

// Database returns the RDB name this connection is attached to.
func (c *Conn) Database() string { return c.rdbnam }
