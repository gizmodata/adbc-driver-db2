package drda

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
)

// FieldDesc is one column entry from a QRYDSC / FDODSC descriptor.
type FieldDesc struct {
	Type   byte   // DRDA FD:OCA type (see types.go)
	Length uint16 // length override: byte length, or precision<<8|scale for decimals
	CCSID  uint32 // from an SDA override, 0 if none
}

// Nullable reports whether the field carries a leading null indicator.
func (f FieldDesc) Nullable() bool { return IsNullable(f.Type) }

// Precision / Scale for decimal fields.
func (f FieldDesc) Precision() int { return int(f.Length >> 8) }
func (f FieldDesc) Scale() int     { return int(f.Length & 0xFF) }

type sdaOverride struct {
	fieldType byte
	ccsid     uint32
	length    uint16
}

// ParseQRYDSC parses the FD:OCA triplet list in a QRYDSC (or FDODSC).
func ParseQRYDSC(body []byte) ([]FieldDesc, error) {
	var fields []FieldDesc
	overrides := map[byte]sdaOverride{}
	pos := 0
	for pos < len(body) {
		if pos+3 > len(body) {
			return nil, fmt.Errorf("drda: truncated FD:OCA triplet at %d", pos)
		}
		tlen := int(body[pos])
		ttype := body[pos+1]
		tid := body[pos+2]
		if tlen < 3 || pos+tlen > len(body) {
			return nil, fmt.Errorf("drda: bad FD:OCA triplet length %d at %d", tlen, pos)
		}
		payload := body[pos+3 : pos+tlen]
		switch ttype {
		case 0x78: // MDD — meta data definition; nothing we need
		case 0x70: // SDA — simple data array override
			if len(payload) < 9 {
				return nil, fmt.Errorf("drda: short SDA triplet")
			}
			overrides[tid] = sdaOverride{
				fieldType: payload[0],
				ccsid:     binary.BigEndian.Uint32(payload[1:5]),
				length:    binary.BigEndian.Uint16(payload[7:9]),
			}
		case 0x76, 0x7F: // N-GDA (id 0xD0) / CPT continuation
			for i := 0; i+3 <= len(payload); i += 3 {
				fd := FieldDesc{Type: payload[i], Length: binary.BigEndian.Uint16(payload[i+1 : i+3])}
				if ov, ok := overrides[fd.Type]; ok {
					fd.Type = ov.fieldType
					fd.CCSID = ov.ccsid
				}
				fields = append(fields, fd)
			}
		case 0x71: // RLO — row layout; fixed shapes, ignore
		default:
			return nil, fmt.Errorf("drda: unknown FD:OCA triplet type 0x%02X", ttype)
		}
		pos += tlen
	}
	return fields, nil
}

// RowDecoder decodes QRYDTA payloads into rows of Values.
type RowDecoder struct {
	Fields       []FieldDesc
	LittleEndian bool
}

// DecodeBlock decodes every row in a QRYDTA body. Each row is prefixed
// by a 2-byte SQLCAGRP indicator (0xFF = null SQLCA → normal row); a
// non-null SQLCA in the row stream ends the block (it carries e.g.
// SQLCODE +100).
//
// A query block may end in the middle of a row; the unconsumed tail is
// returned as leftover so the caller can prepend it to the next block.
//
// Each row is an SQLCADTA: a nullable SQLCAGRP (0xFF when null, else
// a full SQLCA — typically a warning such as SQLSTATE 01003) followed by
// a nullable SQLDTAGRP (0xFF at end of data, else 0x00 and the row).
// Warnings are passed to onWarning; an error SQLCA or SQLCODE +100 ends
// the block and is returned.
func (d *RowDecoder) DecodeBlock(body []byte, emit func([]Value) error, onWarning func(*SQLCA)) (leftover []byte, ca *SQLCA, err error) {
	r := &byteReader{b: body, le: d.LittleEndian}
	for r.remaining() > 0 {
		rowStart := r.pos
		var rowCA *SQLCA
		if r.b[r.pos] == 0xFF {
			r.pos++
		} else {
			rowCA = parseSQLCAGRP(r)
			if r.err != nil {
				if r.truncated {
					return body[rowStart:], nil, nil
				}
				return nil, nil, r.err
			}
			if rowCA.IsError() || rowCA.SQLCode == 100 {
				return nil, rowCA, nil
			}
		}
		ind := r.u8()
		if r.err != nil {
			return body[rowStart:], nil, nil
		}
		if rowCA != nil && onWarning != nil {
			onWarning(rowCA)
		}
		if ind == 0xFF {
			// Null SQLDTAGRP: no row follows.
			return nil, rowCA, nil
		}
		row := make([]Value, len(d.Fields))
		for i, f := range d.Fields {
			row[i] = d.decodeField(r, f)
			if r.err != nil {
				if r.truncated {
					return body[rowStart:], nil, nil
				}
				return nil, nil, r.err
			}
		}
		if err := emit(row); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, nil
}

func (d *RowDecoder) decodeField(r *byteReader, f FieldDesc) Value {
	if f.Nullable() {
		if r.u8() == 0xFF {
			return nil
		}
	}
	base := f.Type &^ 1
	switch base {
	case TypeSmall:
		return int16(r.u16())
	case TypeInteger:
		return r.i32()
	case TypeInteger8:
		return int64(r.u64())
	case Type1ByteInt:
		return int16(int8(r.u8()))
	case TypeFloat4:
		return math.Float32frombits(d.u32(r))
	case TypeFloat8:
		return math.Float64frombits(r.u64())
	case TypeBoolean:
		// Flows as a 2-byte field on Db2 LUW (FD:OCA length 2).
		n := int(f.Length)
		if n == 0 {
			n = 1
		}
		for _, b := range r.take(n) {
			if b != 0 {
				return true
			}
		}
		return false
	case TypeDecimal:
		return decodePackedDecimal(r, f.Precision(), f.Scale())
	case TypeDecFloat:
		dv, neg, special := decodeDecFloat(r.take(int(f.Length)))
		switch special {
		case decFloatNaN:
			return math.NaN()
		case decFloatInf:
			if neg {
				return math.Inf(-1)
			}
			return math.Inf(1)
		}
		return dv
	case TypeDate:
		return parseDate(string(r.take(int(f.Length))))
	case TypeTime:
		return parseTime(string(r.take(int(f.Length))))
	case TypeTimestamp:
		return parseTimestamp(string(r.take(int(f.Length))))
	case TypeChar, TypeMix:
		return d.decodeText(r.take(int(f.Length)), f, true)
	case TypeGraphic:
		// Length is in double-byte characters.
		return d.decodeText(r.take(2*int(f.Length)), f, true)
	case TypeVarChar, TypeLong, TypeVarMix, TypeLongMix:
		n := int(r.u16BE())
		return d.decodeText(r.take(n), f, false)
	case TypeVarGraph, TypeLongGraph:
		n := int(r.u16BE())
		return d.decodeText(r.take(2*n), f, false)
	case TypeFixByte, TypeFixBytes, TypeRowID:
		return cloneBytes(r.take(int(f.Length & 0x7FFF)))
	case TypeVarByte, TypeVarBinary:
		n := int(r.u16BE())
		return cloneBytes(r.take(n))
	case TypeLongVarByte:
		n := int(d.u32BE(r))
		return cloneBytes(r.take(n))
	case TypeLobLoc, TypeClobLoc, TypeDbcsClobLoc:
		// 4-byte locator; the driver does not dereference locators.
		_ = r.take(int(f.Length))
		return LobRef{IsChar: base != TypeLobLoc}
	case TypeLobBytes:
		_ = r.take(int(f.Length & 0x7FFF))
		return LobRef{IsChar: false}
	case TypeLobCSBCS:
		_ = r.take(int(f.Length & 0x7FFF))
		return LobRef{IsChar: true}
	default:
		r.fail("drda: unsupported FD:OCA type 0x%02X", f.Type)
		return nil
	}
}

func (d *RowDecoder) u32(r *byteReader) uint32 {
	v := r.take(4)
	if v == nil {
		return 0
	}
	if d.LittleEndian {
		return binary.LittleEndian.Uint32(v)
	}
	return binary.BigEndian.Uint32(v)
}

func (d *RowDecoder) u32BE(r *byteReader) uint32 {
	v := r.take(4)
	if v == nil {
		return 0
	}
	return binary.BigEndian.Uint32(v)
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// decodeText decodes character data according to the field's CCSID
// (1208 = UTF-8, 1200/17584 = UTF-16BE, else EBCDIC), trimming trailing
// blanks for fixed-width fields.
func (d *RowDecoder) decodeText(b []byte, f FieldDesc, fixed bool) string {
	if b == nil {
		return ""
	}
	var s string
	base := f.Type &^ 1
	isGraphic := base == TypeGraphic || base == TypeVarGraph || base == TypeLongGraph
	switch {
	case isGraphic || f.CCSID == 1200 || f.CCSID == 1201 || f.CCSID == 13488 || f.CCSID == 17584:
		// Graphic data is UTF-16BE (client CCSIDDBC 1200).
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		s = string(utf16.Decode(u))
	default:
		s = decodeMixed(b)
	}
	if fixed {
		s = strings.TrimRight(s, " ")
	}
	return s
}

func decodePackedDecimal(r *byteReader, precision, scale int) Value {
	n := (precision + 2) / 2
	b := r.take(n)
	if b == nil {
		return nil
	}
	digits := make([]byte, 0, precision+1)
	for i, c := range b {
		hi, lo := c>>4, c&0x0F
		if i == len(b)-1 {
			digits = append(digits, '0'+hi)
			neg := lo == 0xD || lo == 0xB
			v, ok := new(big.Int).SetString(strings.TrimLeft(string(digits), "0"), 10)
			if !ok {
				v = big.NewInt(0)
			}
			if neg {
				v.Neg(v)
			}
			return Decimal{Unscaled: v, Scale: int32(scale)}
		}
		digits = append(digits, '0'+hi, '0'+lo)
	}
	return nil
}

func parseDate(s string) Value {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return s
	}
	y, e1 := strconv.Atoi(s[0:4])
	m, e2 := strconv.Atoi(s[5:7])
	dd, e3 := strconv.Atoi(s[8:10])
	if e1 != nil || e2 != nil || e3 != nil {
		return s
	}
	return Date{Year: y, Month: m, Day: dd}
}

func parseTime(s string) Value {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return s
	}
	h, e1 := strconv.Atoi(s[0:2])
	m, e2 := strconv.Atoi(s[3:5])
	sec, e3 := strconv.Atoi(s[6:8])
	if e1 != nil || e2 != nil || e3 != nil {
		return s
	}
	return Time{Hour: h, Minute: m, Second: sec}
}

// parseTimestamp handles "YYYY-MM-DD-HH.MM.SS[.FFFFFFFFFFFF]" (Db2) and
// the ISO "YYYY-MM-DD HH:MM:SS[.F]" form.
func parseTimestamp(s string) Value {
	s = strings.TrimSpace(s)
	if len(s) < 19 {
		return s
	}
	dv, ok := parseDate(s[:10]).(Date)
	if !ok {
		return s
	}
	tv, ok := parseTime(s[11:19]).(Time)
	if !ok {
		return s
	}
	ts := Timestamp{Date: dv, Time: tv}
	if len(s) > 20 {
		frac := s[20:]
		for len(frac) < 9 {
			frac += "0"
		}
		frac = frac[:9]
		if n, err := strconv.Atoi(frac); err == nil {
			ts.Nanos = n
		}
	}
	return ts
}
