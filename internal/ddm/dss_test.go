package ddm

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDSSRoundTripSmall(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.WriteRequest(NewObject(EXCSQLIMM, []byte{1, 2, 3}), 1, true, false)
	w.WriteRequest(NewObject(SQLSTT, []byte("SELECT 1")), 1, false, true)
	require.NoError(t, w.Flush())

	d1, err := ReadDSS(&buf)
	require.NoError(t, err)
	require.Equal(t, EXCSQLIMM, d1.CodePoint)
	require.Equal(t, byte(DSSTypeRequest), d1.Type)
	require.True(t, d1.Chained)
	require.True(t, d1.SameCorrelator)
	require.Equal(t, uint16(1), d1.CorrelationID)
	require.Equal(t, []byte{1, 2, 3}, d1.Payload)

	d2, err := ReadDSS(&buf)
	require.NoError(t, err)
	require.Equal(t, SQLSTT, d2.CodePoint)
	require.Equal(t, byte(DSSTypeObject), d2.Type)
	require.False(t, d2.Chained)
	require.Equal(t, []byte("SELECT 1"), d2.Payload)
	require.Equal(t, 0, buf.Len())
}

func TestDSSRoundTripLarge(t *testing.T) {
	for _, n := range []int{32757, 32758, 40000, 65535, 65536, 100000, 200001} {
		body := make([]byte, n)
		for i := range body {
			body[i] = byte(i * 7)
		}
		var buf bytes.Buffer
		w := NewWriter(&buf)
		w.WriteRequest(NewObject(EXTDTA, body), 2, false, true)
		require.NoError(t, w.Flush())
		d, err := ReadDSS(&buf)
		require.NoError(t, err, "n=%d", n)
		require.Equal(t, EXTDTA, d.CodePoint)
		require.Equal(t, body, d.Payload, "n=%d", n)
		require.Equal(t, 0, buf.Len())
	}
}

func TestDSSReadLayerBStreaming(t *testing.T) {
	// Db2-style unknown-length continuation: DSS length 0xFFFF, object
	// length 0x8004 (no extended length bytes), then segments.
	seg1 := make([]byte, 32767-10)
	seg2 := []byte("tail")
	var raw []byte
	raw = append(raw, 0xFF, 0xFF, 0xD0, 0x02, 0x00, 0x02, 0x80, 0x04, 0x24, 0x1B)
	raw = append(raw, seg1...)
	raw = append(raw, 0x00, byte(2+len(seg2)))
	raw = append(raw, seg2...)
	d, err := ReadDSS(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, QRYDTA, d.CodePoint)
	require.Equal(t, append(append([]byte{}, seg1...), seg2...), d.Payload)
}

func TestUnchainLast(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.WriteRequest(NewObject(RDBCMM, nil), 1, true, false)
	w.UnchainLast()
	require.NoError(t, w.Flush())
	d, err := ReadDSS(&buf)
	require.NoError(t, err)
	require.False(t, d.Chained)
	require.False(t, d.SameCorrelator)
}

func TestEBCDIC(t *testing.T) {
	for _, s := range []string{"", "HELLO", "db2inst1", "Zürich ü", "NULLID SYSSH200"} {
		require.Equal(t, s, DecodeEBCDIC(EncodeEBCDIC(s)))
	}
	require.Equal(t, []byte{0xC8, 0xC9}, EncodeEBCDIC("HI"))
	require.Equal(t, 18, len(PadEBCDIC("TESTDB", 18)))
	require.Equal(t, byte(0x40), PadEBCDIC("TESTDB", 18)[17])
}

func TestParams(t *testing.T) {
	var body []byte
	body = append(body, Uint16(SECMEC, 9)...)
	body = append(body, EBCDIC(RDBNAM, "TESTDB")...)
	body = append(body, Uint64(QRYINSID, 0x1122334455667788)...)
	p, err := ParseParams(body)
	require.NoError(t, err)
	v, ok := p.Uint16(SECMEC)
	require.True(t, ok)
	require.Equal(t, uint16(9), v)
	s, _ := p.EBCDICString(RDBNAM)
	require.Equal(t, "TESTDB", s)
	q, _ := p.Uint64(QRYINSID)
	require.Equal(t, uint64(0x1122334455667788), q)
	require.Len(t, p.All, 3)
	_, err = ParseParams([]byte{0, 1})
	require.Error(t, err)
}
