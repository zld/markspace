package model

import "math"

// This file holds the exact, overflow-checked integer arithmetic backing the
// rational time base. Products are evaluated as unsigned 128-bit values and
// only narrowed to int64 after a range check, so a corrupt timescale cannot
// silently wrap a timestamp.

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		if v == math.MinInt64 {
			return math.MaxInt64
		}
		return -v
	}
	return v
}

// mulNoOverflow computes (a*b, c*d) while detecting int64 overflow.
func mulNoOverflow(a, b, c, d int64) (int64, int64, bool) {
	ah, al := mulFull(a, b)
	ch, cl := mulFull(c, d)
	if !fitsInt64(ah, al) || !fitsInt64(ch, cl) {
		return 0, 0, false
	}
	return int64(al), int64(cl), true
}

// addRat computes an/ad + cn/cd with intermediate-overflow detection. It
// reduces over the least common denominator so repeated additions along a
// sample timeline do not grow the denominator without bound. Let
// g = gcd(ad, cd); then lcm = ad*cd/g and
//
//	num = an*(cd/g) + cn*(ad/g)
//	den = lcm
//
// followed by one final gcd reduction. Every product is 128-bit range
// checked before being narrowed to int64.
func addRat(an, ad, cn, cd int64) (int64, int64, bool) {
	g := gcd(ad, cd) // denominators are positive, so abs is unnecessary
	cdOverG := cd / g
	adOverG := ad / g

	hi1, lo1 := mulFull(an, cdOverG)
	hi2, lo2 := mulFull(cn, adOverG)
	numHi, numLo := add128(hi1, lo1, hi2, lo2)
	if !fitsInt64(numHi, numLo) {
		return 0, 0, false
	}
	denHi, denLo := mulFull(ad, cdOverG) // ad * (cd/g) == lcm(ad, cd)
	if !fitsInt64(denHi, denLo) || denLo == 0 {
		return 0, 0, false
	}
	num, den := int64(numLo), int64(denLo)
	if r := gcd(abs64(num), den); r > 1 {
		num /= r
		den /= r
	}
	return num, den, true
}

// mulFull returns the signed 128-bit product of a and b as unsigned
// high/low words in two's-complement representation. It multiplies the
// absolute magnitudes (which always fit the unsigned 128-bit result) and
// applies sign correction afterwards, so negative inputs cannot create
// spurious high words.
func mulFull(a, b int64) (hi, lo uint64) {
	neg := (a < 0) != (b < 0)
	au := uint64(abs64(a))
	bu := uint64(abs64(b))
	al := au & 0xffffffff
	ah := au >> 32
	bl := bu & 0xffffffff
	bh := bu >> 32
	ll := al * bl
	lh := al * bh
	hl := ah * bl
	hh := ah * bh
	mid := (ll >> 32) + (lh & 0xffffffff) + (hl & 0xffffffff)
	lo = (mid << 32) | (ll & 0xffffffff)
	hi = hh + (lh >> 32) + (hl >> 32) + (mid >> 32)
	if neg {
		// two's-complement negate of the unsigned 128-bit magnitude
		lo = ^lo + 1
		hi = ^hi
		if lo == 0 {
			hi++
		}
	}
	return hi, lo
}

// add128 adds two unsigned 128-bit values and returns the unsigned
// overflow carry (always 0 or 1).
func add128(ahi, alo, bhi, blo uint64) (hi, lo uint64) {
	lo = alo + blo
	carry := uint64(0)
	if lo < alo {
		carry = 1
	}
	hi = ahi + bhi + carry
	return hi, lo
}

// fitsInt64 reports whether the 128-bit two's-complement value fits in int64.
func fitsInt64(hi, lo uint64) bool {
	switch hi {
	case 0:
		return lo <= math.MaxInt64
	case math.MaxUint64:
		return lo >= math.MaxInt64+1
	default:
		return false
	}
}
