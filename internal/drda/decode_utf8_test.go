package drda

import "testing"

// Non-Latin text whose UTF-8 bytes, read as EBCDIC, score as "more
// readable" than the UTF-8 reading, plus inputs that were always fine.
var utf8ColumnSamples = []string{
	"中文",
	"数据库",
	"你好，世界",
	"日本語テキスト",
	"한국어",
	"Grüße",
	"AB中",
	"plain ascii",
	"",
}

// Character column data in a CCSID 1208 (or 0) column is UTF-8 and must
// come back unchanged, for single-byte and mixed-byte FD:OCA types alike.
func TestDecodeTextUTF8Columns(t *testing.T) {
	types := []byte{
		TypeChar, TypeVarChar, TypeLong, TypeCStr, TypeLStr,
		TypeMix, TypeVarMix, TypeLongMix, TypeCStrMix, TypeLStrMix,
	}
	for _, ccsid := range []uint16{1208, 0} {
		d := &RowDecoder{CCSIDSBC: ccsid, CCSIDDBC: 1200, CCSIDMBC: ccsid}
		for _, base := range types {
			for _, typ := range []byte{base, base | 1} { // NOT NULL and nullable
				for _, want := range utf8ColumnSamples {
					if got := d.decodeText([]byte(want), FieldDesc{Type: typ}, false); got != want {
						t.Errorf("ccsid %d, type 0x%02X: decodeText(%q) = %q", ccsid, typ, want, got)
					}
				}
			}
		}
	}
}

// Fixed-width fields keep trimming trailing blanks.
func TestDecodeTextUTF8FixedTrimsBlanks(t *testing.T) {
	d := &RowDecoder{CCSIDSBC: 1208, CCSIDDBC: 1200, CCSIDMBC: 1208}
	if got := d.decodeText([]byte("中文   "), FieldDesc{Type: TypeMix}, true); got != "中文" {
		t.Fatalf("decodeText(fixed) = %q, want %q", got, "中文")
	}
}

// Bytes that are not valid UTF-8 still go through decodeMixed, so
// EBCDIC-encoded text decodes exactly as before.
func TestDecodeUTF8TextInvalidFallsBack(t *testing.T) {
	ebcdicHello := []byte{0xC8, 0x85, 0x93, 0x93, 0x96} // "Hello" in CCSID 500
	if got, want := decodeUTF8Text(ebcdicHello), decodeMixed(ebcdicHello); got != want {
		t.Fatalf("decodeUTF8Text = %q, want decodeMixed's %q", got, want)
	}
	if got := decodeUTF8Text(ebcdicHello); got != "Hello" {
		t.Fatalf("decodeUTF8Text(EBCDIC) = %q, want %q", got, "Hello")
	}
}

// Character LOBs arrive in EXTDTA; their text must be decoded as UTF-8 too.
func TestResolveLOBsUTF8Clob(t *testing.T) {
	want := "你好，世界"
	q := &Query{
		Fields:  []FieldDesc{{Type: TypeLobCSBCS | 1}},
		pending: [][]Value{{LobRef{IsChar: true}}},
		// A nullable LOB's EXTDTA starts with a null-indicator byte (0x00 = not null).
		extdta: [][]byte{append([]byte{0x00}, want...)},
	}
	q.resolveLOBs()
	if got := q.pending[0][0]; got != want {
		t.Fatalf("resolved CLOB = %q, want %q", got, want)
	}
}
