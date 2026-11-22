// Package timeline normalizes multi-standard timecodes (24/25/29.97 DF/NDF/50)
// and SRT/WebVTT cue timestamps onto one shared integer-nanosecond time base.
package timeline

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// NanosPerSecond is the resolution of the shared time base.
const NanosPerSecond int64 = 1_000_000_000

// Standard sentinel errors. Callers can inspect them with errors.Is.
var (
	ErrUnsupportedRate = errors.New("frame rate cannot be represented exactly on the nanosecond base and is not a known standard")
	ErrIllegalFrame    = errors.New("illegal frame number for the declared timecode standard")
	ErrBackward        = errors.New("timecode moves backward")
	ErrMalformed       = errors.New("malformed timecode or cue timestamp")
)

// Rate is an exact rational frame rate: Num frames per Den seconds.
// Drop selects drop-frame label counting (only valid for 30000/1001).
type Rate struct {
	Num  uint32
	Den  uint32
	Drop bool
}

// Named standards. All input labels are canonicalized onto these values.
var (
	Rate24     = Rate{Num: 24, Den: 1}
	Rate25     = Rate{Num: 25, Den: 1}
	Rate2997   = Rate{Num: 30000, Den: 1001}
	Rate2997DF = Rate{Num: 30000, Den: 1001, Drop: true}
	Rate50     = Rate{Num: 50, Den: 1}
)

// ParseRate accepts "24", "25", "29.97", "29.97df", "29.97ndf", "30000/1001",
// "50" and exact integer rates that divide a second (e.g. "30", "60").
// Truncated decimal labels such as "23.976" or "29.970" are rejected instead
// of being silently approximated.
func ParseRate(s string) (Rate, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	drop := false
	switch {
	case strings.HasSuffix(t, "ndf"):
		t = strings.TrimSpace(t[:len(t)-3])
	case strings.HasSuffix(t, "df"):
		drop = true
		t = strings.TrimSpace(t[:len(t)-2])
	}
	switch t {
	case "24":
		if drop {
			return Rate{}, fmt.Errorf("%w: drop-frame is only defined for 30000/1001", ErrUnsupportedRate)
		}
		return Rate24, nil
	case "25":
		if drop {
			return Rate{}, fmt.Errorf("%w: drop-frame is only defined for 30000/1001", ErrUnsupportedRate)
		}
		return Rate25, nil
	case "50":
		if drop {
			return Rate{}, fmt.Errorf("%w: drop-frame is only defined for 30000/1001", ErrUnsupportedRate)
		}
		return Rate50, nil
	case "29.97", "30000/1001":
		r := Rate2997
		r.Drop = drop
		return r, nil
	}
	if strings.Contains(t, "/") {
		p := strings.Split(t, "/")
		if len(p) != 2 {
			return Rate{}, fmt.Errorf("%w: %q", ErrMalformed, s)
		}
		num, err1 := strconv.ParseUint(p[0], 10, 32)
		den, err2 := strconv.ParseUint(p[1], 10, 32)
		if err1 != nil || err2 != nil || num == 0 || den == 0 {
			return Rate{}, fmt.Errorf("%w: %q", ErrMalformed, s)
		}
		return newRate(uint32(num), uint32(den), drop)
	}
	n, err := strconv.ParseUint(t, 10, 32)
	if err != nil || n == 0 {
		return Rate{}, fmt.Errorf("%w: %q", ErrMalformed, s)
	}
	return newRate(uint32(n), 1, drop)
}

// knownApprox lists standards whose frame duration is slightly longer than a
// whole nanosecond count but which the media industry treats as exact labels.
var knownApprox = map[Rate]bool{
	{Num: 30000, Den: 1001}: true, // 29.97 NDF/DF
	{Num: 24, Den: 1}:       true, // film
	{Num: 30, Den: 1}:       true, // integer 30 (coexists with 29.97)
}

func newRate(num, den uint32, drop bool) (Rate, error) {
	if drop && !(num == 30000 && den == 1001) {
		return Rate{}, fmt.Errorf("%w: drop-frame is only defined for 30000/1001", ErrUnsupportedRate)
	}
	r := Rate{Num: num, Den: den, Drop: drop}
	// Custom rates require an exact integer-nanosecond frame duration.
	if !knownApprox[Rate{Num: num, Den: den}] &&
		(uint64(den)*uint64(NanosPerSecond))%uint64(num) != 0 {
		return Rate{}, fmt.Errorf("%w: %d/%d", ErrUnsupportedRate, num, den)
	}
	return r, nil
}

// String renders the canonical rate label.
func (r Rate) String() string {
	switch {
	case r == Rate2997DF:
		return "29.97df"
	case r == Rate2997:
		return "29.97ndf"
	case r.Den == 1:
		return strconv.FormatUint(uint64(r.Num), 10)
	default:
		return fmt.Sprintf("%d/%d", r.Num, r.Den)
	}
}

// Timecode is a wall-clock frame label.
type Timecode struct {
	H, M, S, F int
}

// ParseTimecode accepts HH:MM:SS:FF and HH:MM:SS;FF spellings.
func ParseTimecode(s string) (Timecode, error) {
	t := strings.TrimSpace(s)
	if i := strings.LastIndex(t, ";"); i >= 0 {
		t = t[:i] + ":" + t[i+1:]
	}
	p := strings.Split(t, ":")
	if len(p) != 4 {
		return Timecode{}, fmt.Errorf("%w: %q", ErrMalformed, s)
	}
	v := make([]int, 4)
	for i, part := range p {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || len(part) > 2 {
			return Timecode{}, fmt.Errorf("%w: %q", ErrMalformed, s)
		}
		v[i] = n
	}
	return Timecode{H: v[0], M: v[1], S: v[2], F: v[3]}, nil
}

func (tc Timecode) String() string {
	sep := ":"
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", tc.H, tc.M, tc.S, sep, tc.F)
}

// nominalFramesPerSecond is 30 for the 30000/1001 label convention.
func (r Rate) nominalFPS() int {
	if r.Num == 30000 && r.Den == 1001 {
		return 30
	}
	return int(r.Num) / int(r.Den)
}

// Validate rejects out-of-range fields and the frame labels skipped by DF.
func (r Rate) Validate(tc Timecode) error {
	if tc.M < 0 || tc.M > 59 || tc.S < 0 || tc.S > 59 || tc.H < 0 || tc.H > 99 || tc.F < 0 {
		return fmt.Errorf("%w: %s", ErrIllegalFrame, tc.String())
	}
	if tc.F >= r.nominalFPS() {
		return fmt.Errorf("%w: frame %d >= %d in %s", ErrIllegalFrame, tc.F, r.nominalFPS(), tc.String())
	}
	if r.Drop && tc.S == 0 && tc.M%10 != 0 && (tc.F == 0 || tc.F == 1) {
		return fmt.Errorf("%w: %s is a skipped drop-frame label", ErrIllegalFrame, tc.String())
	}
	return nil
}

// FrameCount converts a validated label into the zero-based frame count.
func (r Rate) FrameCount(tc Timecode) (uint64, error) {
	if err := r.Validate(tc); err != nil {
		return 0, err
	}
	base := uint64(tc.H)*108000 + uint64(tc.M)*1800 + uint64(tc.S)*uint64(r.nominalFPS()) + uint64(tc.F)
	if !r.Drop {
		return base, nil
	}
	totalMinutes := uint64(60*tc.H + tc.M)
	dropped := 2 * (totalMinutes - totalMinutes/10)
	if base < dropped {
		return 0, fmt.Errorf("%w: %s", ErrIllegalFrame, tc.String())
	}
	return base - dropped, nil
}

// Mapping is the result of normalizing one raw label.
type Mapping struct {
	Raw           string
	Canonical     string
	Nanos         int64
	Frame         uint64
	ResidualNanos int64 // signed sub-nanosecond remainder lost in rounding
}

// frameNanos returns rounded nanoseconds and the signed residual (actual-rounded).
func (r Rate) frameNanos(frame uint64) (int64, int64) {
	num := uint64(r.Num)
	numNS := frame * uint64(r.Den) * uint64(NanosPerSecond)
	q, rem := numNS/num, numNS%num
	ns := int64(q)
	res := int64(rem) // positive: actual value is slightly larger
	if rem*2 >= num { // round half up
		ns++
		res -= int64(num)
	}
	return ns, res
}

// Normalize maps one raw timecode label to the shared nanosecond base and
// reports both the original offset and the canonical label.
func (r Rate) Normalize(raw string) (Mapping, error) {
	tc, err := ParseTimecode(raw)
	if err != nil {
		return Mapping{}, err
	}
	frame, err := r.FrameCount(tc)
	if err != nil {
		return Mapping{}, err
	}
	ns, residual := r.frameNanos(frame)
	return Mapping{
		Raw:           raw,
		Canonical:     r.String() + " " + tc.String(),
		Nanos:         ns,
		Frame:         frame,
		ResidualNanos: residual,
	}, nil
}

// Normalizer is a stateful reader that rejects backward timecodes.
type Normalizer struct {
	rate      Rate
	lastNanos int64
	started   bool
}

// NewNormalizer binds a normalizer to one declared frame standard.
func NewNormalizer(rate Rate) *Normalizer { return &Normalizer{rate: rate} }

// Normalize applies the rate mapping and fails if raw moves backward.
func (n *Normalizer) Normalize(raw string) (Mapping, error) {
	m, err := n.rate.Normalize(raw)
	if err != nil {
		return Mapping{}, err
	}
	if n.started && m.Nanos < n.lastNanos {
		return Mapping{}, fmt.Errorf("%w: %s (%d ns) follows %d ns", ErrBackward, raw, m.Nanos, n.lastNanos)
	}
	n.started, n.lastNanos = true, m.Nanos
	return m, nil
}

// Rate reports the bound standard.
func (n *Normalizer) Rate() Rate { return n.rate }
