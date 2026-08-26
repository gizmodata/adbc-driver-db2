package drda

import (
	"encoding/binary"
	"math/big"
)

// IEEE 754-2008 decimal floating point, DPD encoding (DECFLOAT(16) =
// 8 bytes, DECFLOAT(34) = 16 bytes). Decoded to a Decimal with the
// coefficient and a (possibly negative) scale; NaN/Inf are reported via
// the returned flags.

type decFloatSpecial int

const (
	decFloatFinite decFloatSpecial = iota
	decFloatNaN
	decFloatInf
)

func dpdDecode(dpd uint32) (int, int, int) {
	b := func(i uint) int { return int(dpd>>i) & 1 }
	b9, b8, b7, b6, b5, b4, b3, b2, b1, b0 := b(9), b(8), b(7), b(6), b(5), b(4), b(3), b(2), b(1), b(0)
	switch {
	case b3 == 0:
		return b9*4 + b8*2 + b7, b6*4 + b5*2 + b4, b2*4 + b1*2 + b0
	case b2 == 0 && b1 == 0:
		return b9*4 + b8*2 + b7, b6*4 + b5*2 + b4, 8 + b0
	case b2 == 0 && b1 == 1:
		return b9*4 + b8*2 + b7, 8 + b4, b6*4 + b5*2 + b0
	case b2 == 1 && b1 == 0:
		return 8 + b7, b6*4 + b5*2 + b4, b9*4 + b8*2 + b0
	case b6 == 0 && b5 == 0:
		return 8 + b7, 8 + b4, b9*4 + b8*2 + b0
	case b6 == 0 && b5 == 1:
		return 8 + b7, b9*4 + b8*2 + b4, 8 + b0
	case b6 == 1 && b5 == 0:
		return b9*4 + b8*2 + b7, 8 + b4, 8 + b0
	default:
		return 8 + b7, 8 + b4, 8 + b0
	}
}

// decodeDecFloat decodes 8 or 16 DPD bytes.
func decodeDecFloat(data []byte) (Decimal, bool, decFloatSpecial) {
	var bias, groups, expBits int
	switch len(data) {
	case 8:
		bias, groups, expBits = 398, 5, 8
	case 16:
		bias, groups, expBits = 6176, 11, 12
	default:
		return Decimal{}, false, decFloatFinite
	}
	// Work in a big.Int for the 128-bit case.
	w := new(big.Int).SetBytes(data)
	totalBits := len(data) * 8
	coeffBits := groups * 10

	sign := new(big.Int).Rsh(w, uint(totalBits-1)).Uint64() & 1
	g := new(big.Int).Rsh(w, uint(totalBits-6)).Uint64() & 0x1F
	e := new(big.Int).Rsh(w, uint(coeffBits)).Uint64() & (1<<uint(expBits) - 1)
	t := new(big.Int).And(w, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(coeffBits)), big.NewInt(1)))

	if g >= 0x1C {
		if g&0x02 != 0 {
			return Decimal{}, sign == 1, decFloatNaN
		}
		return Decimal{}, sign == 1, decFloatInf
	}
	var biasedExp uint64
	var lead int
	if g >= 0x18 {
		biasedExp = (g&0x03)<<uint(expBits) | e
		lead = 8 + int((g>>2)&1)
	} else {
		biasedExp = (g>>3)<<uint(expBits) | e
		lead = int(g & 0x07)
	}
	digits := make([]byte, 0, 1+groups*3)
	digits = append(digits, byte('0'+lead))
	mask := big.NewInt(0x3FF)
	for i := groups - 1; i >= 0; i-- {
		grp := new(big.Int).Rsh(t, uint(i*10))
		grp.And(grp, mask)
		d2, d1, d0 := dpdDecode(uint32(grp.Uint64()))
		digits = append(digits, byte('0'+d2), byte('0'+d1), byte('0'+d0))
	}
	coeff, _ := new(big.Int).SetString(string(digits), 10)
	if sign == 1 {
		coeff.Neg(coeff)
	}
	exp := int(biasedExp) - bias
	return Decimal{Unscaled: coeff, Scale: int32(-exp)}, sign == 1, decFloatFinite
}

var _ = binary.BigEndian
