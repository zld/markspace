package manifest

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"archivecheck/internal/model"
)

// ParseDecimalSeconds parses a non-negative decimal with at most nine
// fractional digits (the HLS resolution) into an exact rational number of
// seconds. It never goes through float64.
func ParseDecimalSeconds(text string) (model.Rational, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return model.Rational{}, fmt.Errorf("empty duration")
	}
	if strings.HasPrefix(text, "-") {
		return model.Rational{}, fmt.Errorf("negative duration %q", text)
	}
	whole, frac, hasDot := strings.Cut(text, ".")
	if !hasDot {
		frac = ""
	}
	if whole == "" && frac == "" {
		return model.Rational{}, fmt.Errorf("no digits in %q", text)
	}
	if strings.ContainsAny(whole, "+-") {
		return model.Rational{}, fmt.Errorf("bad sign in %q", text)
	}
	if len(frac) > 9 {
		return model.Rational{}, fmt.Errorf("more than 9 fractional digits in %q", text)
	}
	digits := whole + frac
	if digits == "" {
		digits = "0"
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return model.Rational{}, fmt.Errorf("bad number %q: %w", text, err)
	}
	den := int64(1)
	for i := 0; i < len(frac); i++ {
		if frac[i] < '0' || frac[i] > '9' {
			return model.Rational{}, fmt.Errorf("bad digit %q in %q", frac[i], text)
		}
		den *= 10
	}
	return model.NewRational(n, den)
}

func sortedReps(reps []Representation) {
	sort.SliceStable(reps, func(i, j int) bool {
		if reps[i].Bandwidth != reps[j].Bandwidth {
			return reps[i].Bandwidth > reps[j].Bandwidth
		}
		return reps[i].ID < reps[j].ID
	})
}
