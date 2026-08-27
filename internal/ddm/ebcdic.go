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

// DecodeCCSID decodes single-byte text in the given IBM CCSID. UTF-8
// (1208) and the EBCDIC pages 37/500 (and their euro variants 1140/1148)
// are handled exactly; other single-byte pages fall back to Latin-1.
func DecodeCCSID(b []byte, ccsid uint16) string {
	switch ccsid {
	case 0, 1208, 1252, 819:
		if ccsid == 1252 || ccsid == 819 {
			return decodeLatin1(b)
		}
		return string(b)
	case 37, 1140:
		return decodeTable(b, &cp037ToUnicode)
	case 500, 1148:
		return decodeTable(b, &cp500ToUnicode)
	}
	if ccsid >= 256 && ccsid < 1000 {
		// Unknown EBCDIC page: 500 is the closest common denominator for
		// letters and digits.
		return decodeTable(b, &cp500ToUnicode)
	}
	return decodeLatin1(b)
}

func decodeTable(b []byte, tbl *[256]rune) string {
	out := make([]byte, 0, len(b))
	var buf [utf8.UTFMax]byte
	for _, c := range b {
		n := utf8.EncodeRune(buf[:], tbl[c])
		out = append(out, buf[:n]...)
	}
	return string(out)
}

func decodeLatin1(b []byte) string {
	out := make([]byte, 0, len(b))
	var buf [utf8.UTFMax]byte
	for _, c := range b {
		n := utf8.EncodeRune(buf[:], rune(c))
		out = append(out, buf[:n]...)
	}
	return string(out)
}

// IsEBCDIC reports whether a CCSID is an EBCDIC code page.
func IsEBCDIC(ccsid uint16) bool {
	switch ccsid {
	case 37, 273, 277, 278, 280, 284, 285, 297, 500, 871, 1140, 1141, 1142, 1143, 1144, 1145, 1146, 1147, 1148, 1149:
		return true
	}
	return false
}
