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

func dpdEncode(d2, d1, d0 int) uint32 {
	h2, h1, h0 := d2 >= 8, d1 >= 8, d0 >= 8
	v2, v1, v0 := uint32(d2&7), uint32(d1&7), uint32(d0&7)
	switch {
	case !h2 && !h1 && !h0:
		return v2<<7 | v1<<4 | v0
	case !h2 && !h1 && h0:
		return v2<<7 | v1<<4 | 8 | (v0 & 1)
	case !h2 && h1 && !h0:
		return v2<<7 | ((v0>>2)&1)<<6 | ((v0>>1)&1)<<5 | (v1&1)<<4 | 1<<3 | 1<<1 | (v0 & 1)
	case h2 && !h1 && !h0:
		return ((v0>>2)&1)<<9 | ((v0>>1)&1)<<8 | (v2&1)<<7 | v1<<4 | 1<<3 | 1<<2 | (v0 & 1)
	case h2 && h1 && !h0:
		return ((v0>>2)&1)<<9 | ((v0>>1)&1)<<8 | (v2&1)<<7 | (v1&1)<<4 | 1<<3 | 1<<2 | 1<<1 | (v0 & 1)
	case h2 && !h1 && h0:
		return ((v1>>2)&1)<<9 | ((v1>>1)&1)<<8 | (v2&1)<<7 | 1<<5 | (v1&1)<<4 | 1<<3 | 1<<2 | 1<<1 | (v0 & 1)
	case !h2 && h1 && h0:
		return v2<<7 | 1<<6 | (v1&1)<<4 | 1<<3 | 1<<2 | 1<<1 | (v0 & 1)
	default:
		return (v2&1)<<7 | 1<<6 | 1<<5 | (v1&1)<<4 | 1<<3 | 1<<2 | 1<<1 | (v0 & 1)
	}
}

// encodeDecFloat encodes a finite Decimal as 8- or 16-byte DPD.
func encodeDecFloat(d Decimal, nBytes int) []byte {
	var bias, groups, expBits, maxDigits int
	if nBytes == 8 {
		bias, groups, expBits, maxDigits = 398, 5, 8, 16
	} else {
		nBytes = 16
		bias, groups, expBits, maxDigits = 6176, 11, 12, 34
	}
	neg := d.Unscaled.Sign() < 0
	digits := new(big.Int).Abs(d.Unscaled).String()
	exp := -int(d.Scale)
	// Trim excess precision from the right (round toward zero).
	for len(digits) > maxDigits {
		digits = digits[:len(digits)-1]
		exp++
	}
	for len(digits) < maxDigits {
		digits = "0" + digits
	}
	lead := int(digits[0] - '0')
	cont := digits[1:]
	biased := exp + bias
	var g uint64
	if lead >= 8 {
		g = 0x18 | uint64(lead-8)<<2 | uint64(biased>>uint(expBits))
	} else {
		g = uint64(biased>>uint(expBits))<<3 | uint64(lead)
	}
	e := uint64(biased) & (1<<uint(expBits) - 1)
	t := new(big.Int)
	for i := 0; i < groups; i++ {
		t.Lsh(t, 10)
		t.Or(t, big.NewInt(int64(dpdEncode(int(cont[3*i]-'0'), int(cont[3*i+1]-'0'), int(cont[3*i+2]-'0')))))
	}
	totalBits := nBytes * 8
	w := new(big.Int)
	if neg {
		w.SetBit(w, totalBits-1, 1)
	}
	w.Or(w, new(big.Int).Lsh(new(big.Int).SetUint64(g), uint(totalBits-6)))
	w.Or(w, new(big.Int).Lsh(new(big.Int).SetUint64(e), uint(groups*10)))
	w.Or(w, t)
	out := make([]byte, nBytes)
	w.FillBytes(out)
	return out
}
