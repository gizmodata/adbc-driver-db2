package drda

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
)

// Parameter (SQLDTA) encoding.
//
// A parameterized statement is executed by sending the command
// (EXCSQLSTT / OPNQRY) followed by an SQLDTA object containing an FD:OCA
// descriptor (FDODSC) and the data (FDODTA). Each parameter is encoded
// using the nullable DRDA type so a 1-byte null indicator precedes every
// value. LOB values travel out of line in EXTDTA objects chained after
// the SQLDTA; the FDODTA slot then holds only their length.

// paramEncoding describes how one parameter is encoded on the wire.
type paramEncoding struct {
	drdaType byte
	length   uint16 // FD:OCA length override
	lob      bool   // data goes in EXTDTA
	isChar   bool
}

// maxInline is the largest value (in bytes) sent inline in SQLDTA.
const maxInline = 32000

// encodingFor picks the wire encoding for a described parameter.
func encodingFor(p ColumnDesc) (paramEncoding, error) {
	switch p.Base() {
	case SQLTypeSmallint:
		return paramEncoding{drdaType: TypeNSmall, length: 2}, nil
	case SQLTypeInteger:
		return paramEncoding{drdaType: TypeNInteger, length: 4}, nil
	case SQLTypeBigint:
		return paramEncoding{drdaType: TypeNInteger8, length: 8}, nil
	case SQLTypeFloat:
		if p.Length == 4 {
			return paramEncoding{drdaType: TypeNFloat4, length: 4}, nil
		}
		return paramEncoding{drdaType: TypeNFloat8, length: 8}, nil
	case SQLTypeDecimal, SQLTypeNumeric:
		return paramEncoding{drdaType: TypeNDecimal, length: uint16(p.Precision)<<8 | uint16(p.Scale)}, nil
	case SQLTypeDecFloat:
		return paramEncoding{drdaType: TypeNDecFloat, length: uint16(p.Length)}, nil
	case SQLTypeBoolean:
		return paramEncoding{drdaType: TypeNBoolean, length: 1}, nil
	case SQLTypeDate:
		return paramEncoding{drdaType: TypeNDate, length: 10}, nil
	case SQLTypeTime:
		return paramEncoding{drdaType: TypeNTime, length: 8}, nil
	case SQLTypeTimestamp:
		return paramEncoding{drdaType: TypeNTimestamp, length: 32}, nil
	case SQLTypeChar, SQLTypeVarchar, SQLTypeLongVarchar, SQLTypeGraphic, SQLTypeVargraphic,
		SQLTypeLongVargraph, SQLTypeXML, SQLTypeCStr, SQLTypeLStr, SQLTypeClob, SQLTypeDBClob:
		// Character data is sent as mixed (UTF-8) VARCHAR with a 2-byte
		// length, exactly as IBM's JCC driver does; values over 32 KiB
		// are promoted to an out-of-line CLOB (see encodeSQLDTA).
		return paramEncoding{drdaType: TypeNVarMix, length: 0x7FFF, isChar: true}, nil
	case SQLTypeBinary:
		return paramEncoding{drdaType: TypeNFixBytes, length: uint16(p.Length)}, nil
	case SQLTypeVarbinary:
		return paramEncoding{drdaType: TypeNVarBinary, length: uint16(p.Length)}, nil
	case SQLTypeBlob:
		// Small values go inline as VARBINARY; larger ones are promoted
		// to an out-of-line BLOB in encodeSQLDTA.
		return paramEncoding{drdaType: TypeNVarBinary, length: 0x7FFF}, nil
	case SQLTypeRowID:
		return paramEncoding{drdaType: TypeNRowID, length: uint16(p.Length)}, nil
	}
	return paramEncoding{}, fmt.Errorf("drda: unsupported parameter SQLTYPE %d", p.SQLType)
}

// encodeSQLDTA builds the SQLDTA object plus any EXTDTA objects for one
// row of parameter values.
func (c *Conn) encodeSQLDTA(params []ColumnDesc, values []Value) (ddm.Object, []ddm.Object, error) {
	if len(values) != len(params) {
		return nil, nil, fmt.Errorf("drda: statement has %d parameters but %d values were bound", len(params), len(values))
	}
	encs := make([]paramEncoding, len(params))
	for i, p := range params {
		e, err := encodingFor(p)
		if err != nil {
			return nil, nil, err
		}
		// Promote oversized inline values to out-of-line LOBs (EXTDTA).
		if i < len(values) && values[i] != nil {
			switch e.drdaType {
			case TypeNVarBinary:
				if b, ok := toBytes(values[i]); ok && len(b) > maxInline {
					e = paramEncoding{drdaType: TypeNLobBytes, length: 0x8009, lob: true}
				}
			case TypeNVarMix:
				if str, ok := toString(values[i]); ok && len(str) > maxInline {
					e = paramEncoding{drdaType: TypeNLobCMixed, length: 0x8009, lob: true, isChar: true}
				}
			}
		}
		encs[i] = e
	}

	// FDODSC: one N-GDA triplet (len, 0x76, 0xD0, then 3 bytes per column)
	// and the SQLDTA RLO triplet. More than 84 parameters need a CPT
	// continuation triplet.
	var dsc []byte
	n := len(encs)
	first := n
	if first > 84 {
		first = 84
	}
	dsc = append(dsc, byte(3+3*first), 0x76, 0xD0)
	for i := 0; i < first; i++ {
		dsc = append(dsc, encs[i].drdaType, byte(encs[i].length>>8), byte(encs[i].length))
	}
	for off := first; off < n; off += 84 {
		cnt := n - off
		if cnt > 84 {
			cnt = 84
		}
		dsc = append(dsc, byte(3+3*cnt), 0x7F, 0x00)
		for i := off; i < off+cnt; i++ {
			dsc = append(dsc, encs[i].drdaType, byte(encs[i].length>>8), byte(encs[i].length))
		}
	}
	dsc = append(dsc, 0x06, 0x71, 0xE4, 0xD0, 0x00, 0x01)

	// FDODTA: row indicator, then each value with its null indicator.
	dta := []byte{0x00}
	var extdta []ddm.Object
	for i, v := range values {
		if v == nil {
			dta = append(dta, 0xFF)
			continue
		}
		dta = append(dta, 0x00)
		e := encs[i]
		var err error
		dta, err = c.appendParam(dta, e, params[i], v)
		if err != nil {
			return nil, nil, fmt.Errorf("drda: parameter %d: %w", i+1, err)
		}
		if e.lob {
			var b []byte
			if e.isChar {
				str, _ := toString(v)
				b = []byte(str)
			} else {
				b, _ = toBytes(v)
			}
			// EXTDTA: null-indicator byte then the data.
			body := make([]byte, 0, len(b)+1)
			body = append(body, 0x00)
			body = append(body, b...)
			extdta = append(extdta, ddm.NewObject(ddm.EXTDTA, body))
		}
	}
	var body []byte
	body = append(body, ddm.NewObject(ddm.FDODSC, dsc)...)
	body = append(body, ddm.NewObject(ddm.FDODTA, dta)...)
	return ddm.NewObject(ddm.SQLDTA, body), extdta, nil
}

func (c *Conn) appendParam(dta []byte, e paramEncoding, p ColumnDesc, v Value) ([]byte, error) {
	le := c.Server.LittleEndian
	putU16 := func(b []byte, x uint16) []byte {
		if le {
			return binary.LittleEndian.AppendUint16(b, x)
		}
		return binary.BigEndian.AppendUint16(b, x)
	}
	putU32 := func(b []byte, x uint32) []byte {
		if le {
			return binary.LittleEndian.AppendUint32(b, x)
		}
		return binary.BigEndian.AppendUint32(b, x)
	}
	putU64 := func(b []byte, x uint64) []byte {
		if le {
			return binary.LittleEndian.AppendUint64(b, x)
		}
		return binary.BigEndian.AppendUint64(b, x)
	}
	switch e.drdaType {
	case TypeNSmall:
		n, ok := toInt64(v)
		if !ok {
			return nil, typeMismatch(v, "SMALLINT")
		}
		return putU16(dta, uint16(int16(n))), nil
	case TypeNInteger:
		n, ok := toInt64(v)
		if !ok {
			return nil, typeMismatch(v, "INTEGER")
		}
		return putU32(dta, uint32(int32(n))), nil
	case TypeNInteger8:
		n, ok := toInt64(v)
		if !ok {
			return nil, typeMismatch(v, "BIGINT")
		}
		return putU64(dta, uint64(n)), nil
	case TypeNFloat4:
		f, ok := toFloat64(v)
		if !ok {
			return nil, typeMismatch(v, "REAL")
		}
		return putU32(dta, math.Float32bits(float32(f))), nil
	case TypeNFloat8:
		f, ok := toFloat64(v)
		if !ok {
			return nil, typeMismatch(v, "DOUBLE")
		}
		return putU64(dta, math.Float64bits(f)), nil
	case TypeNBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, typeMismatch(v, "BOOLEAN")
		}
		if b {
			return append(dta, 1), nil
		}
		return append(dta, 0), nil
	case TypeNDecimal:
		d, ok := toDecimal(v)
		if !ok {
			return nil, typeMismatch(v, "DECIMAL")
		}
		return appendPackedDecimal(dta, d, int(e.length>>8), int(e.length&0xFF))
	case TypeNDecFloat:
		// Send DECFLOAT as its textual form via a character parameter is
		// not possible here; encode as DPD.
		d, ok := toDecimal(v)
		if !ok {
			return nil, typeMismatch(v, "DECFLOAT")
		}
		return append(dta, encodeDecFloat(d, int(e.length))...), nil
	case TypeNDate:
		s, ok := formatDate(v)
		if !ok {
			return nil, typeMismatch(v, "DATE")
		}
		return append(dta, s...), nil
	case TypeNTime:
		s, ok := formatTime(v)
		if !ok {
			return nil, typeMismatch(v, "TIME")
		}
		return append(dta, s...), nil
	case TypeNTimestamp:
		s, ok := formatTimestamp(v)
		if !ok {
			return nil, typeMismatch(v, "TIMESTAMP")
		}
		return append(dta, ddm.PadASCII(s, 32)...), nil
	case TypeNVarMix:
		s, ok := toString(v)
		if !ok {
			return nil, typeMismatch(v, "VARCHAR")
		}
		dta = binary.BigEndian.AppendUint16(dta, uint16(len(s)))
		return append(dta, s...), nil
	case TypeNFixBytes:
		b, ok := toBytes(v)
		if !ok {
			return nil, typeMismatch(v, "BINARY")
		}
		out := make([]byte, e.length)
		copy(out, b)
		return append(dta, out...), nil
	case TypeNVarBinary, TypeNRowID:
		b, ok := toBytes(v)
		if !ok {
			return nil, typeMismatch(v, "VARBINARY")
		}
		if len(b) > 32672 {
			return nil, fmt.Errorf("binary value of %d bytes exceeds VARBINARY limit; use a BLOB column", len(b))
		}
		dta = binary.BigEndian.AppendUint16(dta, uint16(len(b)))
		return append(dta, b...), nil
	case TypeNLobBytes, TypeNLobCMixed:
		var n int
		if e.isChar {
			s, ok := toString(v)
			if !ok {
				return nil, typeMismatch(v, "CLOB")
			}
			n = len(s)
		} else {
			b, ok := toBytes(v)
			if !ok {
				return nil, typeMismatch(v, "BLOB")
			}
			n = len(b)
		}
		// 0x8009 placeholder: a 0x02 flag byte and the 8-byte length; the
		// bytes themselves follow in EXTDTA.
		dta = append(dta, 0x02)
		return binary.BigEndian.AppendUint64(dta, uint64(n)), nil
	}
	return nil, fmt.Errorf("unsupported parameter encoding 0x%02X", e.drdaType)
}

func typeMismatch(v Value, want string) error {
	return fmt.Errorf("cannot bind %T as %s", v, want)
}

func toInt64(v Value) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case float32:
		return int64(x), true
	case float64:
		return int64(x), true
	case Decimal:
		if x.Scale == 0 && x.Unscaled.IsInt64() {
			return x.Unscaled.Int64(), true
		}
	}
	return 0, false
}

func toFloat64(v Value) (float64, bool) {
	switch x := v.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case Decimal:
		f, _ := new(big.Float).SetInt(x.Unscaled).Float64()
		return f / math.Pow10(int(x.Scale)), true
	}
	if n, ok := toInt64(v); ok {
		return float64(n), true
	}
	return 0, false
}

func toDecimal(v Value) (Decimal, bool) {
	switch x := v.(type) {
	case Decimal:
		return x, true
	case string:
		return parseDecimalString(x)
	case float32, float64:
		f, _ := toFloat64(v)
		return parseDecimalString(fmtFloat(f))
	}
	if n, ok := toInt64(v); ok {
		return Decimal{Unscaled: big.NewInt(n), Scale: 0}, true
	}
	return Decimal{}, false
}

func fmtFloat(f float64) string {
	return big.NewFloat(f).Text('f', -1)
}

func parseDecimalString(s string) (Decimal, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Decimal{}, false
	}
	neg := false
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
	}
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i+1:]
	}
	digits := intPart + frac
	if digits == "" {
		return Decimal{}, false
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Decimal{}, false
	}
	if neg {
		n.Neg(n)
	}
	return Decimal{Unscaled: n, Scale: int32(len(frac))}, true
}

func toString(v Value) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	case Decimal:
		return x.String(), true
	case Date, Time:
		return fmt.Sprint(x), true
	case Timestamp:
		return x.ToTime().Format("2006-01-02-15.04.05.000000000"), true
	case bool:
		if x {
			return "1", true
		}
		return "0", true
	case float32, float64:
		f, _ := toFloat64(v)
		return fmtFloat(f), true
	}
	if n, ok := toInt64(v); ok {
		return big.NewInt(n).String(), true
	}
	return "", false
}

func toBytes(v Value) ([]byte, bool) {
	switch x := v.(type) {
	case []byte:
		return x, true
	case string:
		return []byte(x), true
	}
	return nil, false
}

func formatDate(v Value) (string, bool) {
	switch x := v.(type) {
	case Date:
		return x.String(), true
	case Timestamp:
		return x.Date.String(), true
	case string:
		return x, true
	}
	return "", false
}

func formatTime(v Value) (string, bool) {
	switch x := v.(type) {
	case Time:
		return x.String(), true
	case Timestamp:
		return x.Time.String(), true
	case string:
		return x, true
	}
	return "", false
}

func formatTimestamp(v Value) (string, bool) {
	switch x := v.(type) {
	case Timestamp:
		return fmt.Sprintf("%04d-%02d-%02d-%02d.%02d.%02d.%09d000", x.Year, x.Month, x.Day, x.Hour, x.Minute, x.Second, x.Nanos), true
	case Date:
		return fmt.Sprintf("%04d-%02d-%02d-00.00.00.000000000000", x.Year, x.Month, x.Day), true
	case string:
		return x, true
	}
	return "", false
}

// appendPackedDecimal encodes d with the described precision/scale as
// packed BCD with a trailing sign nibble (0xC positive, 0xD negative).
func appendPackedDecimal(dta []byte, d Decimal, precision, scale int) ([]byte, error) {
	n := new(big.Int).Set(d.Unscaled)
	switch {
	case int(d.Scale) < scale:
		n.Mul(n, new(big.Int).Exp(bigTen, big.NewInt(int64(scale-int(d.Scale))), nil))
	case int(d.Scale) > scale:
		n.Quo(n, new(big.Int).Exp(bigTen, big.NewInt(int64(int(d.Scale)-scale)), nil))
	}
	neg := n.Sign() < 0
	n.Abs(n)
	digits := n.String()
	if len(digits) > precision {
		return nil, fmt.Errorf("decimal %s does not fit DECIMAL(%d,%d)", d, precision, scale)
	}
	for len(digits) < precision {
		digits = "0" + digits
	}
	if len(digits)%2 == 0 {
		digits = "0" + digits
	}
	nibbles := make([]byte, 0, len(digits)+1)
	for _, ch := range digits {
		nibbles = append(nibbles, byte(ch-'0'))
	}
	if neg {
		nibbles = append(nibbles, 0xD)
	} else {
		nibbles = append(nibbles, 0xC)
	}
	for i := 0; i < len(nibbles); i += 2 {
		dta = append(dta, nibbles[i]<<4|nibbles[i+1])
	}
	return dta, nil
}

var bigTen = big.NewInt(10)

// ---- execution with parameters ----

// paramStatement is a prepared statement whose parameter descriptors
// are known.
type ParamStatement struct {
	conn    *Conn
	sql     string
	Columns []ColumnDesc
	Params  []ColumnDesc
}

// PrepareParams prepares sql and describes its input parameters.
func (c *Conn) PrepareParams(ctx context.Context, sql string) (*ParamStatement, error) {
	cols, params, err := c.Describe(ctx, sql)
	if err != nil {
		return nil, err
	}
	return &ParamStatement{conn: c, sql: sql, Columns: cols, Params: params}, nil
}

// ExecBatch executes the prepared statement once per row of values. All
// rows in the batch are pipelined in a single transmission (chained
// EXCSQLSTT + SQLDTA pairs), so the round-trip cost is paid once per
// batch, not once per row. Returns the total affected-row count.
//
// The statement must have been prepared on this connection since the
// last PRPSQLSTT (the package section is shared).
func (ps *ParamStatement) ExecBatch(ctx context.Context, rows [][]Value) (int64, error) {
	c := ps.conn
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureNoOpenQuery(ctx); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	c.trace("sql (batch of %d): %s", len(rows), ps.sql)
	// Re-prepare so the section holds this statement (a previous
	// Describe/Query may have replaced it).
	c.send(ctx, c.packPRPSQLSTT(c.pkgSN), 1, true, false)
	c.send(ctx, c.packSQLSTT(ps.sql), 1, false, false)
	corr := uint16(2)
	for _, row := range rows {
		sqldta, ext, err := c.encodeSQLDTA(ps.Params, row)
		if err != nil {
			c.wr.Reset()
			return 0, err
		}
		c.send(ctx, c.packEXCSQLSTT(c.pkgSN), corr, true, false)
		last := len(ext) == 0
		c.send(ctx, sqldta, corr, !last, false)
		for i, e := range ext {
			c.send(ctx, e, corr, i < len(ext)-1, false)
		}
		corr++
	}
	// Terminate the chain: re-mark the final DSS as last by sending an
	// RDBCMM? No — instead the writer's last DSS must carry no chain
	// flag. We appended every DSS as chained, so patch the final one.
	c.wr.UnchainLast()
	if err := c.flush(ctx); err != nil {
		return 0, err
	}
	replies, err := c.readChain(ctx, corr-1)
	if err != nil {
		return 0, err
	}
	var total int64
	var firstErr error
	for _, d := range replies {
		switch d.CodePoint {
		case ddm.SQLCARD:
			ca, perr := ParseSQLCARD(d.Payload, c.Server.LittleEndian)
			if perr != nil {
				return 0, perr
			}
			if ca == nil {
				continue
			}
			if ca.IsError() {
				if firstErr == nil {
					firstErr = ca
				}
				continue
			}
			if d.CorrelationID >= 2 {
				total += ca.RowCount()
			}
		case ddm.SQLDARD:
			ca, _, perr := ParseSQLDARD(d.Payload, c.Server.LittleEndian)
			if perr != nil {
				return 0, perr
			}
			if ca.IsError() && firstErr == nil {
				firstErr = ca
			}
		case ddm.SQLERRRM, ddm.RDBUPDRM, ddm.ENDUOWRM:
		default:
			if e := c.replyError(d); e != nil && firstErr == nil {
				firstErr = e
			}
		}
	}
	if firstErr != nil {
		return total, firstErr
	}
	return total, nil
}

// QueryParams opens a cursor for the prepared statement with one row of
// parameter values.
func (ps *ParamStatement) QueryParams(ctx context.Context, values []Value) (*Query, error) {
	c := ps.conn
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureNoOpenQuery(ctx); err != nil {
		return nil, err
	}
	c.trace("sql (params): %s", ps.sql)
	sqldta, ext, err := c.encodeSQLDTA(ps.Params, values)
	if err != nil {
		return nil, err
	}
	q := &Query{conn: c}
	q.decoder.LittleEndian = c.Server.LittleEndian
	q.Columns = ps.Columns
	c.send(ctx, c.packPRPSQLSTT(c.pkgSN), 1, true, false)
	c.send(ctx, c.packSQLSTT(ps.sql), 1, false, false)
	c.send(ctx, c.packOPNQRY(c.pkgSN, true), 2, true, false)
	c.send(ctx, sqldta, 2, len(ext) > 0, len(ext) == 0)
	for i, e := range ext {
		c.send(ctx, e, 2, i < len(ext)-1, i == len(ext)-1)
	}
	if err := c.flush(ctx); err != nil {
		return nil, err
	}
	replies, err := c.readChain(ctx, 2)
	if err != nil {
		return nil, err
	}
	if err := q.consume(replies); err != nil {
		return nil, err
	}
	if !q.opened {
		return nil, fmt.Errorf("drda: OPNQRY with parameters did not open a cursor")
	}
	c.openQuery = q
	return q, nil
}
