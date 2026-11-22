package model

import (
	"math/big"
	"math/bits"
	"strconv"
	"strings"
)

// Rat is an exact signed rational number num/den with den > 0.
// All time math in the pipeline uses integer rationals; converting tick
// values to floating point before comparison is never allowed.
type Rat struct {
	num int64
	den int64 // always strictly positive
}

// ZeroRat is the canonical zero value (0/1).
var ZeroRat = Rat{num: 0, den: 1}

// IntRat wraps a signed integer as a rational.
func IntRat(n int64) Rat { return Rat{num: n, den: 1} }

// NewRat builds a normalized rational. den must be positive.
func NewRat(num, den int64) (Rat, error) {
	if den <= 0 {
		return Rat{}, Errorf(CategoryInput, "INVALID_VALUE",
			"rational denominator must be positive, got %d", den)
	}
	if num == 0 {
		return ZeroRat, nil
	}
	g := int64(gcdU64(magnitude(num), uint64(den)))
	return Rat{num: num / g, den: den / g}, nil
}

// ParseRat accepts "a/b" or a signed integer "a". Decimal fractions are
// rejected on purpose so callers cannot smudge precision.
func ParseRat(s string) (Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rat{}, Errorf(CategoryInput, "INVALID_VALUE", "empty rational value")
	}
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 1:
		n, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return Rat{}, Errorf(CategoryInput, "INVALID_VALUE",
				"invalid integer %q: %v", parts[0], err)
		}
		return IntRat(n), nil
	case 2:
		n, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return Rat{}, Errorf(CategoryInput, "INVALID_VALUE",
				"invalid numerator %q: %v", parts[0], err)
		}
		d, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Rat{}, Errorf(CategoryInput, "INVALID_VALUE",
				"invalid denominator %q: %v", parts[1], err)
		}
		return NewRat(n, d)
	default:
		return Rat{}, Errorf(CategoryInput, "INVALID_VALUE",
			"rational %q must look like a/b", s)
	}
}

// Num returns the numerator.
func (r Rat) Num() int64 { return r.num }

// Den returns the denominator (always positive).
func (r Rat) Den() int64 { return r.den }

// IsZero reports whether the value is zero.
func (r Rat) IsZero() bool { return r.num == 0 }

// Compare returns -1, 0 or 1 like bytes.Compare, without float conversion.
func (r Rat) Compare(o Rat) (int, error) {
	if lhs, ok1 := mul64(r.num, o.den); ok1 {
		if rhs, ok2 := mul64(o.num, r.den); ok2 {
			switch {
			case lhs < rhs:
				return -1, nil
			case lhs > rhs:
				return 1, nil
			default:
				return 0, nil
			}
		}
	}
	br := big.NewRat(r.num, r.den)
	bo := big.NewRat(o.num, o.den)
	return br.Cmp(bo), nil
}

// Add returns r+o, or an overflow error if the reduced result does not
// fit in int64 numerator/denominator.
func (r Rat) Add(o Rat) (Rat, error) { return r.combine(o, true) }

// Sub returns r-o.
func (r Rat) Sub(o Rat) (Rat, error) { return r.combine(o, false) }

func (r Rat) combine(o Rat, add bool) (Rat, error) {
	sign := int64(-1)
	if add {
		sign = 1
	}
	if p1, ok1 := mul64(r.num, o.den); ok1 {
		if p2, ok2 := mul64(o.num, r.den); ok2 {
			if n, ok3 := mul64(sign, p2); ok3 {
				if num, ok4 := add64(p1, n); ok4 {
					if den, ok5 := mul64(r.den, o.den); ok5 {
						return NewRat(num, den)
					}
				}
			}
		}
	}
	br := big.NewRat(r.num, r.den)
	bo := big.NewRat(o.num, o.den)
	var res *big.Rat
	if add {
		res = br.Add(br, bo)
	} else {
		res = br.Sub(br, bo)
	}
	return ratFromBig(res)
}

// Mul returns r*o.
func (r Rat) Mul(o Rat) (Rat, error) {
	if n, ok1 := mul64(r.num, o.num); ok1 {
		if d, ok2 := mul64(r.den, o.den); ok2 {
			return NewRat(n, d)
		}
	}
	res := new(big.Rat).Mul(big.NewRat(r.num, r.den), big.NewRat(o.num, o.den))
	return ratFromBig(res)
}

// Abs returns the absolute value.
func (r Rat) Abs() (Rat, error) {
	if r.num >= 0 {
		return r, nil
	}
	if n, ok := mul64(r.num, -1); ok {
		return Rat{num: n, den: r.den}, nil
	}
	res := new(big.Rat).Abs(big.NewRat(r.num, r.den))
	return ratFromBig(res)
}

// String renders the canonical "p/q" form (or "p" when den == 1).
func (r Rat) String() string {
	if r.den == 1 {
		return strconv.FormatInt(r.num, 10)
	}
	return strconv.FormatInt(r.num, 10) + "/" + strconv.FormatInt(r.den, 10)
}

// DecimalString renders an exact terminating decimal with at most
// maxDigits fractional digits; it falls back to the fractional form when
// no short terminating decimal exists.
func (r Rat) DecimalString(maxDigits int) string {
	if r.den == 1 {
		return strconv.FormatInt(r.num, 10)
	}
	neg := r.num < 0
	mag := magnitude(r.num)
	pow := uint64(1)
	for k := 1; k <= maxDigits; k++ {
		pow *= 10
		if pow%uint64(r.den) == 0 {
			scaled, ok := mul64(int64(mag), int64(pow))
			if !ok {
				return r.String()
			}
			whole := scaled / r.den
			frac := scaled % r.den
			s := strconv.FormatInt(whole, 10) + "." +
				padLeft(strconv.FormatInt(frac, 10), k)
			if neg {
				s = "-" + s
			}
			return s
		}
	}
	return r.String()
}

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}

func ratFromBig(br *big.Rat) (Rat, error) {
	num := br.Num()
	den := br.Denom()
	if !num.IsInt64() || !den.IsInt64() {
		return Rat{}, Errorf(CategoryAlignment, "OVERFLOW",
			"rational result exceeds int64 range")
	}
	return NewRat(num.Int64(), den.Int64())
}

func magnitude(x int64) uint64 {
	if x >= 0 {
		return uint64(x)
	}
	return uint64(-(x + 1)) + 1
}

func gcdU64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// mul64 returns a*b and false on signed int64 overflow.
func mul64(a, b int64) (int64, bool) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	res := int64(lo)
	// The result fits int64 only when the high word equals the sign
	// extension of the low word (all zero or all one).
	if hi != uint64(res>>63) {
		return 0, false
	}
	return res, true
}

// add64 returns a+b and false on signed int64 overflow.
func add64(a, b int64) (int64, bool) {
	lo, carry := bits.Add64(uint64(a), uint64(b), 0)
	res := int64(lo)
	if (a >= 0 && b >= 0 && res < 0) || (a < 0 && b < 0 && (res >= 0 || carry != 1)) {
		return 0, false
	}
	return res, true
}
