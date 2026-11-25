package clock

import (
	"fmt"
	"math"
)

// Stamp is one parsed RTP timestamp and its original packet location.
type Stamp struct {
	Raw      uint32 `json:"raw"`
	Sequence uint16 `json:"sequence"`
	Offset   int64  `json:"byte_offset"`
}

// UnwrappedStamp retains the raw stamp alongside its expanded 64-bit tick
// value and shared-basis nanosecond time.
type UnwrappedStamp struct {
	Stamp
	Ticks int64 `json:"ticks"`
	NS    int64 `json:"ns"`
	Valid bool  `json:"valid"`
}

// UnwrapReport is the monotonic expansion plus every non-silent anomaly.
type UnwrapReport struct {
	ClockRate uint32           `json:"clock_rate"`
	Samples   []UnwrappedStamp `json:"samples"`
	WrapCount int              `json:"wrap_count"`
	Anomalies []Anomaly        `json:"anomalies"`
}

// DefaultMaxStepTicks is one second at a nominal 90 kHz RTP media clock.
const DefaultMaxStepTicks uint32 = 90_000

// UnwrapRTP expands 32-bit RTP timestamps. A clear forward wrap increments
// WrapCount exactly once. Duplicates, backward steps, and steps that cannot
// be uniquely classified are emitted as anomalies rather than hidden.
func UnwrapRTP(stamps []Stamp, clockRate, maxStep uint32) *UnwrapReport {
	if maxStep == 0 {
		maxStep = DefaultMaxStepTicks
	}
	rep := &UnwrapReport{ClockRate: clockRate, Samples: make([]UnwrappedStamp, 0, len(stamps))}
	if clockRate == 0 {
		rep.Anomalies = append(rep.Anomalies, Anomaly{
			Kind: CodeInvalidTimebase, Severity: "failure", Offset: -1,
			Message: "RTP clock rate must not be zero",
		})
		return rep
	}
	if len(stamps) == 0 {
		return rep
	}

	firstNS, err := Convert(int64(stamps[0].Raw), clockRate)
	if err != nil {
		rep.Anomalies = append(rep.Anomalies, AnomalyFromError(err, "failure"))
	}
	prev := UnwrappedStamp{Stamp: stamps[0], Ticks: int64(stamps[0].Raw), NS: firstNS, Valid: true}
	rep.Samples = append(rep.Samples, prev)

	for i := 1; i < len(stamps); i++ {
		curStamp := stamps[i]
		// Unsigned subtraction is the modulo-2^32 forward distance.
		forward := int64(uint32(curStamp.Raw - prev.Raw))
		cur := UnwrappedStamp{Stamp: curStamp, Ticks: prev.Ticks + forward, Valid: true}
		if ns, err := Convert(cur.Ticks, clockRate); err == nil {
			cur.NS = ns
		} else {
			cur.Valid = false
			rep.Anomalies = append(rep.Anomalies, AnomalyFromError(err, "failure"))
		}

		addAnomaly := func(code Code, severity, msg string) {
			a := Anomaly{
				Kind: code, Severity: severity,
				Offset: prev.Offset, EndOffset: cur.Offset,
				StartNS: prev.NS, EndNS: cur.NS,
				Message: msg,
			}
			if cur.NS >= prev.NS {
				a.StartNS, a.EndNS = prev.NS, cur.NS
			} else {
				a.StartNS, a.EndNS = cur.NS, prev.NS
			}
			rep.Anomalies = append(rep.Anomalies, a)
		}

		switch {
		case curStamp.Raw == prev.Raw:
			addAnomaly(CodeDuplicateTimestamp, "warning",
				fmt.Sprintf("duplicate RTP timestamp raw=%d seq=%d..%d", curStamp.Raw, prev.Sequence, curStamp.Sequence))

		case curStamp.Raw < prev.Raw:
			backward := int64(prev.Raw) - int64(curStamp.Raw)
			forwardPlausible := forward <= int64(maxStep)
			backwardPlausible := backward <= int64(maxStep)
			switch {
			case forwardPlausible && !backwardPlausible:
				rep.WrapCount++
			case backwardPlausible && !forwardPlausible:
				cur.Valid = false
				addAnomaly(CodeRegression, "error", fmt.Sprintf(
					"RTP timestamp regressed by %d ticks without a plausible wrap", backward))
			default:
				cur.Valid = false
				addAnomaly(CodeAmbiguousWrap, "error", fmt.Sprintf(
					"cannot uniquely classify backward raw step: forward_wrap=%d ticks, backward=%d ticks, max_step=%d",
					forward, backward, maxStep))
			}

		default:
			if forward > int64(maxStep) {
				cur.Valid = false
				addAnomaly(CodeAmbiguousWrap, "error", fmt.Sprintf(
					"forward step %d exceeds max plausible step %d; lost wraparound or corrupted timestamp",
					forward, maxStep))
			}
		}

		rep.Samples = append(rep.Samples, cur)
		prev = cur
	}
	return rep
}

// Monotonic reports whether all expanded valid samples are nondecreasing.
func (r *UnwrapReport) Monotonic() bool {
	for i := 1; i < len(r.Samples); i++ {
		if r.Samples[i].Ticks < r.Samples[i-1].Ticks {
			return false
		}
	}
	return true
}

// WrapInterval returns the media-time interval of the idx-th unique wrap.
// Wrap indices are one-based in reporting helpers.
func (r *UnwrapReport) WrapInterval(wrapOrdinal int) (startNS, endNS int64, offset, endOffset int64, ok bool) {
	seen := 0
	for i := 1; i < len(r.Samples); i++ {
		a, b := r.Samples[i-1], r.Samples[i]
		if b.Raw < a.Raw && int64(uint32(b.Raw-a.Raw)) <= int64(DefaultMaxStepTicks) &&
			int64(a.Raw)-int64(b.Raw) > int64(DefaultMaxStepTicks) {
			seen++
			if seen == wrapOrdinal {
				return a.NS, b.NS, a.Offset, b.Offset, true
			}
		}
	}
	return 0, 0, -1, -1, false
}

// Ensure math is referenced even if future edits remove direct use.
var _ = math.MaxInt64
