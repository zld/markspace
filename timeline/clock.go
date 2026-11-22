// Package timeline implements the deterministic media timeline engine:
// rational media timebases, event watermarks and bounded replay of late data.
package timeline

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// Error is the shared, code-bearing error model used by every mediadist
// subsystem. Callers can extract the stable machine code with CodeOf.
type Error struct {
	Code    string
	Message string
	Fields  map[string]any
}

// NewError builds a coded error. The fields map is copied so callers can
// reuse their maps safely.
func NewError(code, message string, fields map[string]any) *Error {
	cp := make(map[string]any, len(fields))
	for k, v := range fields {
		cp[k] = v
	}
	return &Error{Code: code, Message: message, Fields: cp}
}

func (e *Error) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, e.Fields[k]))
	}
	if len(parts) == 0 {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + " (" + strings.Join(parts, " ") + ")"
}

// AsError extracts a *timeline.Error from err, if present.
func AsError(err error) (*Error, bool) {
	var te *Error
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}

// CodeOf returns the stable error code of err, or "" when err is nil or
// not a coded error.
func CodeOf(err error) string {
	if te, ok := AsError(err); ok {
		return te.Code
	}
	return ""
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

var bigMax = big.NewInt(int64(^uint64(0) >> 1))
var bigMin = new(big.Int).Neg(bigMax)

func fitsInt64(v *big.Int) bool {
	return v.Cmp(bigMin) >= 0 && v.Cmp(bigMax) <= 0
}

// Rat is a signed rational media timestamp: Num ticks expressed in the
// scale Den. Den is always positive and fractions are stored reduced so
// equal timestamps have identical representations.
type Rat struct {
	Num int64
	Den int64
}

// NewRat validates the denominator, reduces the fraction and checks for
// integer overflow.
func NewRat(num, den int64) (Rat, error) {
	if den <= 0 {
		return Rat{}, NewError("INVALID_TIMESCALE",
			"timebase denominator must be positive",
			map[string]any{"denominator": den})
	}
	if num == 0 {
		return Rat{Num: 0, Den: 1}, nil
	}
	g := gcd64(abs64(num), den)
	r := Rat{Num: num / g, Den: den / g}
	if r.Den <= 0 {
		return Rat{}, NewError("TIMESCALE_OVERFLOW", "rational reduction overflow",
			map[string]any{"num": num, "den": den})
	}
	return r, nil
}

// MustNewRat panics on invalid input; intended for tests and constants.
func MustNewRat(num, den int64) Rat {
	r, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// IsZero reports whether the timestamp equals zero.
func (r Rat) IsZero() bool { return r.Num == 0 }

// Cmp returns -1, 0 or 1 like bytes.Compare, without float conversion.
func (r Rat) Cmp(o Rat) int {
	a := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den))
	b := new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den))
	return a.Cmp(b)
}

func (r Rat) binOp(o Rat, sign int64) (Rat, error) {
	n1 := big.NewInt(r.Num)
	d1 := big.NewInt(r.Den)
	n2 := big.NewInt(o.Num * sign)
	d2 := big.NewInt(o.Den)
	num := new(big.Int).Add(new(big.Int).Mul(n1, d2), new(big.Int).Mul(n2, d1))
	den := new(big.Int).Mul(d1, d2)
	if !fitsInt64(num) || !fitsInt64(den) {
		return Rat{}, NewError("TIMESCALE_OVERFLOW", "rational arithmetic overflow",
			map[string]any{"a": r.String(), "b": o.String()})
	}
	return NewRat(num.Int64(), den.Int64())
}

// Add returns r+o with overflow detection.
func (r Rat) Add(o Rat) (Rat, error) { return r.binOp(o, 1) }

// Sub returns r-o with overflow detection.
func (r Rat) Sub(o Rat) (Rat, error) { return r.binOp(o, -1) }

// MulInt scales r by an integer factor.
func (r Rat) MulInt(k int64) (Rat, error) {
	num := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(k))
	if !fitsInt64(num) {
		return Rat{}, NewError("TIMESCALE_OVERFLOW", "rational multiply overflow",
			map[string]any{"rat": r.String(), "factor": k})
	}
	return NewRat(num.Int64(), r.Den)
}

// ScaleTo returns the integer tick count of r expressed in targetDen. It is
// an error when the conversion is not exact, preventing silent truncation.
func (r Rat) ScaleTo(targetDen int64) (int64, error) {
	if targetDen <= 0 {
		return 0, NewError("INVALID_TIMESCALE", "target scale must be positive",
			map[string]any{"target": targetDen})
	}
	num := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(targetDen))
	q, rem := new(big.Int).QuoRem(num, big.NewInt(r.Den), new(big.Int))
	if rem.Sign() != 0 || !fitsInt64(q) {
		return 0, NewError("NON_EXACT_SCALE", "timestamp does not map exactly to target scale",
			map[string]any{"rat": r.String(), "target_den": targetDen})
	}
	return q.Int64(), nil
}

// Float64 converts to a floating point value for reporting only; ordering
// decisions must use Cmp.
func (r Rat) Float64() float64 {
	f, _ := new(big.Rat).SetFrac(big.NewInt(r.Num), big.NewInt(r.Den)).Float64()
	return f
}

// String renders the canonical "Num/Den" form.
func (r Rat) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }
