package ddm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// DSS (Data Stream Structure) framing.
//
// Every DDM message travels in a DSS envelope:
//
//	+0  uint16  DSS length (including this 6-byte header)
//	+2  byte    0xD0 (DDM identifier)
//	+3  byte    format: bits 0-3 type, bit 6 chained, bit 4 same-correlator
//	+4  uint16  request correlation id
//	+6  ...     one DDM object: uint16 length, uint16 code point, payload
//
// A DSS longer than 32767 bytes is "continued": the length field is 0xFFFF
// (or the high bit is set) and the object payload is split into
// successive segments each prefixed by a 2-byte length whose high bit
// indicates another segment follows.

const (
	dssMagic = 0xD0

	// DSS format-byte type nibble.
	DSSTypeRequest             = 0x1 // RQSDSS
	DSSTypeReply               = 0x2 // RPYDSS
	DSSTypeObject              = 0x3 // OBJDSS
	DSSTypeCommunications      = 0x4
	DSSTypeRequestContinuation = 0x5
	DSSTypeReplyContinuation   = 0x6

	dssFlagChained        = 0x40 // another DSS follows in this transmission
	dssFlagSameCorrelator = 0x10 // next DSS carries the same correlation id
	dssFlagContinued      = 0x80 // this DSS is continued (length field is not final)

	// MaxDSSLength is the largest single (uncontinued) DSS.
	MaxDSSLength = 32767
	// Continuation segments carry at most this much payload (2-byte
	// length prefix included).
	maxSegmentLength = 32767
)

// DSS is a single decoded Data Stream Structure.
type DSS struct {
	Type           byte
	Chained        bool
	SameCorrelator bool
	CorrelationID  uint16
	CodePoint      CodePoint
	// Payload is the DDM object body: everything after the 4-byte
	// (length, code point) object header, with continuation segments
	// already spliced together.
	Payload []byte
}

// ErrInvalidDSS is returned when the stream does not carry a DDM DSS.
var ErrInvalidDSS = errors.New("ddm: invalid DSS header")

// ReadDSS reads exactly one DSS (with all its continuation segments)
// from r.
func ReadDSS(r io.Reader) (*DSS, error) {
	var hdr [10]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("ddm: read DSS header: %w", err)
	}
	if hdr[2] != dssMagic {
		return nil, fmt.Errorf("%w: magic byte 0x%02X, header % X", ErrInvalidDSS, hdr[2], hdr[:])
	}
	dssLen := int(binary.BigEndian.Uint16(hdr[0:2]))
	format := hdr[3]
	d := &DSS{
		Type:           format & 0x0F,
		Chained:        format&dssFlagChained != 0,
		SameCorrelator: format&dssFlagSameCorrelator != 0,
		CorrelationID:  binary.BigEndian.Uint16(hdr[4:6]),
	}
	objLen := int(binary.BigEndian.Uint16(hdr[6:8]))
	d.CodePoint = CodePoint(binary.BigEndian.Uint16(hdr[8:10]))

	continued := dssLen == 0xFFFF || objLen&0x8000 != 0
	if !continued {
		if objLen < 4 || objLen != dssLen-6 {
			return nil, fmt.Errorf("%w: DSS length %d, object length %d", ErrInvalidDSS, dssLen, objLen)
		}
		d.Payload = make([]byte, objLen-4)
		if _, err := io.ReadFull(r, d.Payload); err != nil {
			return nil, fmt.Errorf("ddm: read DSS payload: %w", err)
		}
		return d, nil
	}

	// Continued DSS. The first segment's object length has its high bit
	// set; its actual length is (objLen & 0x7FFF). Then a chain of
	// [uint16 len][data] segments follows until one arrives without the
	// high bit set.
	firstLen := objLen & 0x7FFF
	if dssLen == 0xFFFF {
		// Db2 LUW sends 0xFFFF DSS length with an object length of
		// 0x8004 (= 4 | continued) for the first segment; the segment
		// then runs to the 32767-byte DSS boundary.
		firstLen = MaxDSSLength - 6
	}
	if firstLen < 4 {
		return nil, fmt.Errorf("%w: continued first segment length %d", ErrInvalidDSS, firstLen)
	}
	payload := make([]byte, 0, 64*1024)
	buf := make([]byte, firstLen-4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("ddm: read DSS first segment: %w", err)
	}
	payload = append(payload, buf...)
	for {
		var lb [2]byte
		if _, err := io.ReadFull(r, lb[:]); err != nil {
			return nil, fmt.Errorf("ddm: read DSS continuation length: %w", err)
		}
		segLen := int(binary.BigEndian.Uint16(lb[:]))
		more := segLen&0x8000 != 0
		segLen &= 0x7FFF
		if segLen < 2 {
			return nil, fmt.Errorf("%w: continuation segment length %d", ErrInvalidDSS, segLen)
		}
		seg := make([]byte, segLen-2)
		if _, err := io.ReadFull(r, seg); err != nil {
			return nil, fmt.Errorf("ddm: read DSS continuation: %w", err)
		}
		payload = append(payload, seg...)
		if !more {
			break
		}
	}
	d.Payload = payload
	return d, nil
}

// Object is an encoded DDM object (uint16 length, uint16 code point, body).
type Object []byte

// NewObject builds a DDM object with the given code point and body.
func NewObject(cp CodePoint, body []byte) Object {
	o := make([]byte, 4+len(body))
	binary.BigEndian.PutUint16(o[0:2], uint16(4+len(body)))
	binary.BigEndian.PutUint16(o[2:4], uint16(cp))
	copy(o[4:], body)
	return o
}

// CodePoint returns the object's code point.
func (o Object) CodePoint() CodePoint {
	return CodePoint(binary.BigEndian.Uint16(o[2:4]))
}

// Body returns the object's payload (after the 4-byte header).
func (o Object) Body() []byte { return o[4:] }

// Writer batches DSSs for one request transmission. DSSs written with
// Chained=true are marked chained; Flush sends the whole buffer.
type Writer struct {
	w   io.Writer
	buf []byte
}

// NewWriter wraps w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// WriteRequest appends a request or object DSS carrying obj to the
// pending transmission. The DSS type is chosen from the code point:
// command data objects (SQLSTT, SQLATTR, SQLDTA, EXTDTA) are OBJDSS,
// everything else is RQSDSS. Objects larger than MaxDSSLength are
// automatically split into continuation segments.
func (wr *Writer) WriteRequest(obj Object, correlationID uint16, sameCorrelatorNext bool, last bool) {
	typ := byte(DSSTypeRequest)
	switch obj.CodePoint() {
	case SQLSTT, SQLATTR, SQLDTA, EXTDTA:
		typ = DSSTypeObject
	}
	format := typ
	if !last {
		format |= dssFlagChained
	}
	if sameCorrelatorNext {
		format |= dssFlagSameCorrelator
	}

	total := len(obj) + 6
	if total <= MaxDSSLength {
		var hdr [6]byte
		binary.BigEndian.PutUint16(hdr[0:2], uint16(total))
		hdr[2] = dssMagic
		hdr[3] = format
		binary.BigEndian.PutUint16(hdr[4:6], correlationID)
		wr.buf = append(wr.buf, hdr[:]...)
		wr.buf = append(wr.buf, obj...)
		return
	}

	// Continued DSS: first segment fills a full 32767-byte DSS; the
	// object length has its high bit set. Remaining bytes follow in
	// segments of up to 32767 bytes (2-byte length prefix included), with
	// the high bit set on every segment except the last.
	var hdr [6]byte
	binary.BigEndian.PutUint16(hdr[0:2], uint16(MaxDSSLength))
	hdr[2] = dssMagic
	hdr[3] = format
	binary.BigEndian.PutUint16(hdr[4:6], correlationID)
	wr.buf = append(wr.buf, hdr[:]...)
	firstBody := MaxDSSLength - 6
	var objHdr [4]byte
	binary.BigEndian.PutUint16(objHdr[0:2], uint16(firstBody)|0x8000)
	binary.BigEndian.PutUint16(objHdr[2:4], uint16(obj.CodePoint()))
	wr.buf = append(wr.buf, objHdr[:]...)
	body := obj.Body()
	wr.buf = append(wr.buf, body[:firstBody-4]...)
	rest := body[firstBody-4:]
	for len(rest) > 0 {
		n := len(rest)
		if n > maxSegmentLength-2 {
			n = maxSegmentLength - 2
		}
		segLen := uint16(n + 2)
		if n < len(rest) {
			segLen |= 0x8000
		}
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], segLen)
		wr.buf = append(wr.buf, lb[:]...)
		wr.buf = append(wr.buf, rest[:n]...)
		rest = rest[n:]
	}
}

// Flush writes every pending DSS to the underlying writer.
func (wr *Writer) Flush() error {
	if len(wr.buf) == 0 {
		return nil
	}
	_, err := wr.w.Write(wr.buf)
	wr.buf = wr.buf[:0]
	return err
}

// Pending returns the number of buffered bytes (for tests).
func (wr *Writer) Pending() int { return len(wr.buf) }
