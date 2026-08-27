package drda

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReplayCapturedQuery decodes QRYDSC/QRYDTA payloads captured from a
// real server (hex dumps from adbc.db2.trace=hex) when REPLAY_DIR is set.
func TestReplayCapturedQuery(t *testing.T) {
	dir := os.Getenv("REPLAY_DIR")
	if dir == "" {
		t.Skip("REPLAY_DIR not set")
	}
	load := func(name string) []byte {
		b, err := os.ReadFile(dir + "/" + name + ".hex")
		require.NoError(t, err)
		raw, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", ""))
		require.NoError(t, err)
		return raw
	}
	if b, err := os.ReadFile(dir + "/SQLDARD.hex"); err == nil {
		raw, _ := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", ""))
		variant := SQLDAIBMi
		if os.Getenv("REPLAY_LE") == "1" {
			variant = SQLDALUW
		}
		ca, cols, err := ParseSQLDARDVariant(raw, os.Getenv("REPLAY_LE") == "1", variant, 37)
		require.NoError(t, err)
		t.Logf("sqldard: ca=%v columns=%d", ca, len(cols))
		for i, col := range cols {
			if i < 12 || i >= len(cols)-2 {
				t.Logf("  col %d %+v", i, col)
			}
		}
	}
	fields, err := ParseQRYDSC(load("QRYDSC"))
	require.NoError(t, err)
	t.Logf("fields: %d", len(fields))
	dec := RowDecoder{Fields: fields, LittleEndian: os.Getenv("REPLAY_LE") == "1", CCSIDSBC: 37, CCSIDMBC: 1208, CCSIDDBC: 835}
	var rows [][]Value
	leftover, ca, err := dec.DecodeBlock(load("QRYDTA"), func(r []Value) error { rows = append(rows, r); return nil }, nil)
	require.NoError(t, err)
	t.Logf("rows=%d leftover=%d sqlca=%v", len(rows), len(leftover), ca)
	for i, r := range rows {
		if i > 1 {
			break
		}
		for j, v := range r {
			if j < 25 {
				t.Logf("  row %d col %d type=0x%02X len=%d -> %T %q", i, j, fields[j].Type, fields[j].Length, v, v)
			}
		}
	}
	if b, err := os.ReadFile(dir + "/SQLCARD.hex"); err == nil {
		raw, _ := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", ""))
		ca, err := ParseSQLCARD(raw, dec.LittleEndian)
		t.Logf("sqlcard: %+v err=%v", ca, err)
	}
}
