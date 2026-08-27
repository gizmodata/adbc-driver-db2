package drda

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZonedDecimal(t *testing.T) {
	// EBCDIC zoned 00123.45 with positive sign nibble C: F0 F0 F1 F2 F3 F4 C5
	r := &byteReader{b: []byte{0xF0, 0xF0, 0xF1, 0xF2, 0xF3, 0xF4, 0xC5}}
	v := decodeZonedDecimal(r, 7, 2)
	require.Equal(t, "123.45", v.(Decimal).String())
	// negative (D zone), ASCII digits also accepted
	r = &byteReader{b: []byte{0x31, 0x32, 0xD3}}
	require.Equal(t, "-123", decodeZonedDecimal(r, 3, 0).(Decimal).String())
	// ASCII negative zone 0x7
	r = &byteReader{b: []byte{0x30, 0x35, 0x71}}
	require.Equal(t, "-0.51", decodeZonedDecimal(r, 3, 2).(Decimal).String())
	// round trip through the encoder
	enc, err := appendZonedDecimal(nil, Decimal{Unscaled: big.NewInt(-12345), Scale: 2}, 7, 2)
	require.NoError(t, err)
	require.Equal(t, []byte{0xF0, 0xF0, 0xF1, 0xF2, 0xF3, 0xF4, 0xD5}, enc)
	require.Equal(t, "-123.45", decodeZonedDecimal(&byteReader{b: enc}, 7, 2).(Decimal).String())
	// too wide
	_, err = appendZonedDecimal(nil, Decimal{Unscaled: big.NewInt(123456), Scale: 0}, 3, 0)
	require.Error(t, err)
}
