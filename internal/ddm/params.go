package ddm

import (
	"encoding/binary"
	"fmt"
)

// Parameter-object helpers. A DDM command's body is a sequence of
// (uint16 length, uint16 code point, value) triplets; these build them.

// Bytes appends a parameter carrying raw bytes.
func Bytes(cp CodePoint, v []byte) []byte {
	b := make([]byte, 4+len(v))
	binary.BigEndian.PutUint16(b[0:2], uint16(4+len(v)))
	binary.BigEndian.PutUint16(b[2:4], uint16(cp))
	copy(b[4:], v)
	return b
}

// Uint16 appends a 2-byte big-endian integer parameter.
func Uint16(cp CodePoint, v uint16) []byte {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], v)
	return Bytes(cp, buf[:])
}

// Uint32 appends a 4-byte big-endian integer parameter.
func Uint32(cp CodePoint, v uint32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return Bytes(cp, buf[:])
}

// Uint64 appends an 8-byte big-endian integer parameter.
func Uint64(cp CodePoint, v uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	return Bytes(cp, buf[:])
}

// Byte appends a 1-byte parameter.
func Byte(cp CodePoint, v byte) []byte { return Bytes(cp, []byte{v}) }

// EBCDIC appends a string parameter encoded as CCSID 500.
func EBCDIC(cp CodePoint, s string) []byte { return Bytes(cp, EncodeEBCDIC(s)) }

// Params is a parsed parameter list (code point → raw value). Repeated
// code points keep the last value in Map and every value in All.
type Params struct {
	Map map[CodePoint][]byte
	All []Param
}

// Param is one (code point, value) pair in order of appearance.
type Param struct {
	CodePoint CodePoint
	Value     []byte
}

// ParseParams splits a reply-message or reply-data body into its
// parameter objects.
func ParseParams(body []byte) (*Params, error) {
	p := &Params{Map: make(map[CodePoint][]byte)}
	for len(body) > 0 {
		if len(body) < 4 {
			return nil, fmt.Errorf("ddm: truncated parameter (%d bytes left)", len(body))
		}
		ln := int(binary.BigEndian.Uint16(body[0:2]))
		cp := CodePoint(binary.BigEndian.Uint16(body[2:4]))
		if ln < 4 || ln > len(body) {
			return nil, fmt.Errorf("ddm: bad parameter length %d for %s (%d bytes left)", ln, cp, len(body))
		}
		v := body[4:ln]
		p.Map[cp] = v
		p.All = append(p.All, Param{CodePoint: cp, Value: v})
		body = body[ln:]
	}
	return p, nil
}

// Uint16 returns the 2-byte value for cp (0, false if absent).
func (p *Params) Uint16(cp CodePoint) (uint16, bool) {
	v, ok := p.Map[cp]
	if !ok || len(v) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(v[:2]), true
}

// Uint64 returns the 8-byte value for cp (0, false if absent).
func (p *Params) Uint64(cp CodePoint) (uint64, bool) {
	v, ok := p.Map[cp]
	if !ok || len(v) < 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(v[:8]), true
}

// EBCDICString returns the CCSID-500-decoded value for cp.
func (p *Params) EBCDICString(cp CodePoint) (string, bool) {
	v, ok := p.Map[cp]
	if !ok {
		return "", false
	}
	return DecodeEBCDIC(v), true
}
