package validate

import (
	"errors"

	"mediaforensics/internal/clock"
)

// errBadRational is returned when a manifest frame time is not a n/d fraction.
var errBadRational = errors.New("validate: invalid rational frame time")

// scanRational parses the canonical "num/den" text emitted by clock.Rational.
// It is allocation-light and rejects anything the renderer could not produce.
func scanRational(text string, num, den *int64) (int, error) {
	slash := -1
	for i := 0; i < len(text); i++ {
		if text[i] == '/' {
			slash = i
			break
		}
	}
	if slash <= 0 || slash == len(text)-1 {
		return 0, errBadRational
	}
	n, err := parseSignedInt64(text[:slash])
	if err != nil {
		return 0, err
	}
	d, err := parseSignedInt64(text[slash+1:])
	if err != nil || d <= 0 {
		return 0, errBadRational
	}
	*num, *den = n, d
	return len(text), nil
}

func parseSignedInt64(s string) (int64, error) {
	if len(s) == 0 {
		return 0, errBadRational
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	var v int64
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errBadRational
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, nil
}

// Ensure clock stays referenced for the documented time-base contract.
var _ = clock.Zero
