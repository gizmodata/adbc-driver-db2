package drda

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// SQLCA is the parsed SQL communications area from a SQLCARD / SQLDARD.
type SQLCA struct {
	SQLCode  int32
	SQLState string
	SQLErrp  string
	SQLErrd  [6]int32
	SQLWarn  [11]byte
	RDBName  string
	Message  string
}

// Error implements error; only meaningful when SQLCode < 0.
func (s *SQLCA) Error() string {
	msg := s.Message
	if msg == "" {
		msg = "SQL error"
	}
	return fmt.Sprintf("SQLCODE=%d SQLSTATE=%s: %s", s.SQLCode, s.SQLState, msg)
}

// IsError reports whether the SQLCA carries a negative SQLCODE.
func (s *SQLCA) IsError() bool { return s != nil && s.SQLCode < 0 }

// IsWarning reports whether the SQLCA carries a positive SQLCODE.
func (s *SQLCA) IsWarning() bool { return s != nil && s.SQLCode > 0 }

// RowCount returns SQLERRD(3), the number of rows affected by a DML
// statement.
func (s *SQLCA) RowCount() int64 {
	if s == nil {
		return 0
	}
	return int64(s.SQLErrd[2])
}

// byteReader is a bounds-checked cursor over a reply-data body.
type byteReader struct {
	b   []byte
	pos int
	err error
	le  bool // numeric fields little-endian (QTDSQLX86)
	// truncated is set when a read ran past the end of the buffer.
	truncated bool
}

func (r *byteReader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

func (r *byteReader) remaining() int { return len(r.b) - r.pos }

func (r *byteReader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.pos+n > len(r.b) {
		r.truncated = true
		r.fail("drda: truncated reply data (want %d bytes at %d of %d)", n, r.pos, len(r.b))
		return nil
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v
}

func (r *byteReader) u8() byte {
	v := r.take(1)
	if v == nil {
		return 0
	}
	return v[0]
}

func (r *byteReader) u16() uint16 {
	v := r.take(2)
	if v == nil {
		return 0
	}
	if r.le {
		return binary.LittleEndian.Uint16(v)
	}
	return binary.BigEndian.Uint16(v)
}

func (r *byteReader) u16BE() uint16 {
	v := r.take(2)
	if v == nil {
		return 0
	}
	return binary.BigEndian.Uint16(v)
}

func (r *byteReader) i32() int32 {
	v := r.take(4)
	if v == nil {
		return 0
	}
	if r.le {
		return int32(binary.LittleEndian.Uint32(v))
	}
	return int32(binary.BigEndian.Uint32(v))
}

func (r *byteReader) u64() uint64 {
	v := r.take(8)
	if v == nil {
		return 0
	}
	if r.le {
		return binary.LittleEndian.Uint64(v)
	}
	return binary.BigEndian.Uint64(v)
}

// vcs reads a VCS/VCM string: uint16 (big-endian) length then bytes.
// Db2 sends mixed (VCM) strings as UTF-8 and single-byte (VCS) strings
// in EBCDIC; we try UTF-8 first and fall back to CCSID 500.
func (r *byteReader) vcs() string {
	n := int(r.u16BE())
	if n == 0 {
		return ""
	}
	return decodeMixed(r.take(n))
}

// vcmOrVcs reads the (VCM, VCS) pair convention: two length-prefixed
// strings of which at most one is non-empty.
func (r *byteReader) vcmOrVcs() string {
	m := r.vcs()
	s := r.vcs()
	if m != "" {
		return m
	}
	return s
}

// ParseSQLCARD parses a SQLCARD reply-data body. It returns nil when
// the SQLCAGRP is null (flag 0xFF) and the reader positioned after it.
func ParseSQLCARD(body []byte, littleEndian bool) (*SQLCA, error) {
	r := &byteReader{b: body, le: littleEndian}
	ca := parseSQLCAGRP(r)
	if r.err != nil {
		return nil, r.err
	}
	return ca, nil
}

func parseSQLCAGRP(r *byteReader) *SQLCA {
	if r.u8() == 0xFF {
		return nil
	}
	ca := &SQLCA{}
	ca.SQLCode = r.i32()
	ca.SQLState = string(r.take(5))
	ca.SQLErrp = strings.TrimRight(string(r.take(8)), " ")
	// SQLCAXGRP
	if r.u8() != 0xFF {
		for i := range ca.SQLErrd {
			ca.SQLErrd[i] = r.i32()
		}
		copy(ca.SQLWarn[:], r.take(11))
		ca.RDBName = r.vcs()
		msgM := r.vcs()
		msgS := r.vcs()
		msg := msgM
		if msg == "" {
			msg = msgS
		}
		// Db2 separates message tokens with 0xFF.
		ca.Message = strings.ReplaceAll(msg, "ÿ", ", ")
	}
	// SQLDIAGGRP — null (0xFF) in practice; a non-null group would need
	// the full diagnostics grammar. Consume the flag and stop.
	if r.remaining() > 0 {
		if r.u8() != 0xFF {
			// Unknown non-null diagnostics; leave the remainder unparsed.
			r.pos = len(r.b)
		}
	}
	return ca
}

func decodeMixed(b []byte) string {
	if isValidUTF8Text(b) {
		return string(b)
	}
	return decodeEBCDICText(b)
}
