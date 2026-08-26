package ddm

import "unicode/utf8"

var unicodeToCP500 = func() map[rune]byte {
	m := make(map[rune]byte, 256)
	for i, r := range cp500ToUnicode {
		m[r] = byte(i)
	}
	return m
}()

// EncodeEBCDIC converts s to EBCDIC CCSID 500. Runes that have no CP500
// mapping are replaced with the EBCDIC substitute character (0x3F).
func EncodeEBCDIC(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if b, ok := unicodeToCP500[r]; ok {
			out = append(out, b)
		} else {
			out = append(out, 0x3F)
		}
	}
	return out
}

// DecodeEBCDIC converts EBCDIC CCSID 500 bytes to a Go (UTF-8) string.
func DecodeEBCDIC(b []byte) string {
	out := make([]byte, 0, len(b))
	var buf [utf8.UTFMax]byte
	for _, c := range b {
		n := utf8.EncodeRune(buf[:], cp500ToUnicode[c])
		out = append(out, buf[:n]...)
	}
	return string(out)
}

// PadEBCDIC encodes s as EBCDIC and right-pads with EBCDIC spaces (0x40)
// or truncates to exactly n bytes. Used for fixed-width fields such as
// RDBNAM (18 bytes).
func PadEBCDIC(s string, n int) []byte {
	b := EncodeEBCDIC(s)
	if len(b) >= n {
		return b[:n]
	}
	out := make([]byte, n)
	copy(out, b)
	for i := len(b); i < n; i++ {
		out[i] = 0x40
	}
	return out
}

// PadASCII right-pads s with spaces or truncates to exactly n bytes.
func PadASCII(s string, n int) []byte {
	b := []byte(s)
	if len(b) >= n {
		return b[:n]
	}
	out := make([]byte, n)
	copy(out, b)
	for i := len(b); i < n; i++ {
		out[i] = ' '
	}
	return out
}
