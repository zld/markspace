package media

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// NS is the universal integer nanosecond time base.
type NS int64

const (
	Nanosecond  NS = 1
	Microsecond NS = 1000 * Nanosecond
	Millisecond NS = 1000 * Microsecond
	Second      NS = 1000 * Millisecond
)

// MaxInputBytes bounds a single local manifest or media input read.
const MaxInputBytes int64 = 64 << 20

// CheckedMulNS returns a*unit while rejecting int64 overflow.
func CheckedMulNS(value int64, unit NS) (int64, error) {
	if value == 0 || unit == 0 {
		return 0, nil
	}
	if value > math.MaxInt64/int64(unit) || value < math.MinInt64/int64(unit) {
		return 0, NewFault(PhaseTimeline, CodeTimeOverflow,
			fmt.Sprintf("multiply %d by %d overflows int64 nanoseconds", value, unit))
	}
	return value * int64(unit), nil
}

// DurationFromTimescale converts duration in timescale ticks to integer NS,
// rounding to nearest. Overflow is reported with the shared fault model.
func DurationFromTimescale(ticks int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, NewFault(PhaseTimeline, CodeIllegalDuration, "timescale must be non-zero")
	}
	if ticks < 0 {
		return 0, NewFault(PhaseTimeline, CodeIllegalDuration, "duration ticks must be non-negative")
	}
	// ticks*1e9 can be much larger than int64 for malicious DASH values.
	q, rem := ticks/int64(timescale), ticks%int64(timescale)
	if q > (math.MaxInt64-int64(Second)/2)/int64(Second) {
		return 0, NewFault(PhaseTimeline, CodeTimeOverflow,
			fmt.Sprintf("%d ticks at timescale %d overflows NS", ticks, timescale))
	}
	base, err := CheckedMulNS(q, Second)
	if err != nil {
		return 0, err
	}
	frac := rem * int64(Second)
	if rem != 0 && frac/rem != int64(Second) {
		return 0, NewFault(PhaseTimeline, CodeTimeOverflow, "fractional timescale conversion overflow")
	}
	den := int64(timescale)
	rounded := (frac + den/2) / den
	if base > math.MaxInt64-rounded {
		return 0, NewFault(PhaseTimeline, CodeTimeOverflow, "rounded duration overflows NS")
	}
	return base + rounded, nil
}

// ParseSecondsToNS parses an EXTINF decimal duration without float error.
func ParseSecondsToNS(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, NewFault(PhaseManifest, CodeIllegalDuration, "empty duration")
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	parts := strings.SplitN(s, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, NewFault(PhaseManifest, CodeIllegalDuration, "bad seconds: "+s)
	}
	ns, err := CheckedMulNS(whole, Second)
	if err != nil {
		return 0, err
	}
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 9 {
			frac = frac[:9]
		}
		frac = frac + strings.Repeat("0", 9-len(frac))
		n, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, NewFault(PhaseManifest, CodeIllegalDuration, "bad fractional duration: "+s)
		}
		ns += n
	}
	if negative {
		if ns == math.MinInt64 {
			return 0, NewFault(PhaseManifest, CodeTimeOverflow, "negative duration overflow")
		}
		ns = -ns
	}
	if ns <= 0 {
		return 0, NewFault(PhaseManifest, CodeIllegalDuration, "duration must be positive: "+s)
	}
	return ns, nil
}

// UnwrapModulo expands a wrapping integer. prev is the previous absolute
// value; raw is the low-bit counter. At an exact half-window tie the direction
// is ambiguous and an error is returned rather than guessing.
func UnwrapModulo(prev int64, raw uint64, bits uint) (int64, error) {
	if bits == 0 || bits >= 64 {
		return 0, NewFault(PhaseTimeline, CodeInvalidArgument, "bits must be in 1..63")
	}
	mod := uint64(1) << bits
	half := int64(mod >> 1)
	r := int64(raw) // raw < mod <= 1<<63 for bits <= 63
	current := prev - (prev % int64(mod)) + r
	delta := current - prev
	if delta > half {
		current -= int64(mod)
	} else if delta < -half {
		current += int64(mod)
	} else if delta == half && raw != uint64(prev%int64(mod)) {
		return 0, NewFault(PhaseTimeline, CodeAmbiguousJump,
			fmt.Sprintf("exact half-window jump from %d to raw %d", prev, raw))
	}
	if (current > 0 && prev > math.MaxInt64-(current-prev)) ||
		(current < 0 && prev < math.MinInt64-(current-prev)) {
		return 0, NewFault(PhaseTimeline, CodeTimeOverflow, "unwrapped counter overflows int64")
	}
	return current, nil
}

// UnwrapSequence16 expands a 16-bit RTP sequence number into a monotonic
// 64-bit sequence. The first packet initializes the counter.
func UnwrapSequence16(prev *uint64, seq uint16) uint64 {
	const window = uint64(1) << 16
	if *prev == 0 && seq != 0 {
		// Use seq+1 so zero remains the "uninitialized" sentinel.
		*prev = uint64(seq) + 1
		return uint64(seq)
	}
	last := *prev - 1
	candidate := last&^(window-1) + uint64(seq)
	if candidate+window/2 < last {
		candidate += window
	} else if candidate > last+window/2 {
		candidate -= window
	}
	*prev = candidate + 1
	return candidate
}
