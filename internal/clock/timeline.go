package clock

import (
	"sync"
	"time"
)

// TimeBase is the fixed media clock used by segmentation, silence detection,
// manifests and checkpoints. Nanoseconds are sufficient for 20 ms frame
// boundaries and keep JSON output unit-explicit.
type TimeBase = time.Duration

const (
	Nanosecond  TimeBase = 1
	Microsecond          = 1000 * Nanosecond
	Millisecond          = 1000 * Microsecond
	Second               = 1000 * Millisecond
)

// Point is one packet's position on the common timeline.
type Point struct {
	SSRC        uint32        `json:"ssrc"`
	Sequence    uint16        `json:"sequence"`
	ExtSequence uint64        `json:"ext_sequence"`
	Timestamp   uint32        `json:"rtp_timestamp"`
	ExtTS       uint64        `json:"ext_timestamp"`
	MediaTime   time.Duration `json:"media_time_ns"`
	Arrival     time.Time     `json:"-"`
	WallTime    time.Time     `json:"wall_time,omitempty"`
	ClockSource string        `json:"clock_source"`
}

// SenderReport is the clock-relevant subset of an RTCP sender report.
type SenderReport struct {
	SSRC         uint32
	NTPTime      time.Time
	RTPTime      uint32
	PacketCount  uint32
	OctetCount   uint32
	SourceOffset int64
}

type wrapState struct {
	highestExtTS  uint64
	haveTS        bool
	originExtTS   uint64
	hasOriginTS   bool
	highestExtSeq uint64
	haveSeq       bool
}

type correction struct {
	report     SenderReport
	extRTPTS   uint64
	receivedAt time.Time
}

// Timeline unwraps 16-bit sequence numbers and 32-bit RTP timestamps and maps
// every SSRC to one monotonic media clock. It is safe for concurrent use.
type Timeline struct {
	mu         sync.Mutex
	SampleRate uint32

	states      map[uint32]*wrapState
	reports     map[uint32]correction
	clockSource map[uint32]string
	issues      []*Error
	baseArrival time.Time
}

func NewTimeline(sampleRate uint32) *Timeline {
	if sampleRate == 0 {
		sampleRate = 48000
	}
	return &Timeline{
		SampleRate:  sampleRate,
		states:      make(map[uint32]*wrapState),
		reports:     make(map[uint32]correction),
		clockSource: make(map[uint32]string),
		baseArrival: time.Unix(0, 0).UTC(),
	}
}

func (t *Timeline) state(ssrc uint32) *wrapState {
	st := t.states[ssrc]
	if st == nil {
		st = &wrapState{}
		t.states[ssrc] = st
	}
	return st
}

// ExtendTimestamp maps a wrapping uint32 timestamp to the nearest monotonic
// value around the highest value already observed. This remains correct when a
// packet immediately after wrap is processed before a late pre-wrap packet.
func (t *Timeline) ExtendTimestamp(ssrc uint32, ts uint32) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(ssrc)
	v := uint64(ts)
	if !st.haveTS {
		st.haveTS = true
		st.highestExtTS = v
		return v
	}
	const modulus = uint64(1) << 32
	best := v + nearestCycle(v, st.highestExtTS, modulus)
	if best > st.highestExtTS {
		st.highestExtTS = best
	}
	return best
}

// ExtendSequence performs the same nearest-cycle unwrapping for uint16 seq.
func (t *Timeline) ExtendSequence(ssrc uint32, seq uint16) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(ssrc)
	v := uint64(seq)
	if !st.haveSeq {
		st.haveSeq = true
		st.highestExtSeq = v
		return v
	}
	const modulus = uint64(1) << 16
	best := v + nearestCycle(v, st.highestExtSeq, modulus)
	if best > st.highestExtSeq {
		st.highestExtSeq = best
	}
	return best
}

func nearestCycle(lowValue, highReference, modulus uint64) uint64 {
	cycle := highReference / modulus
	bestCycle := cycle
	bestDistance := distance(lowValue+cycle*modulus, highReference)
	if cycle > 0 {
		if d := distance(lowValue+(cycle-1)*modulus, highReference); d < bestDistance {
			bestCycle = cycle - 1
			bestDistance = d
		}
	}
	if d := distance(lowValue+(cycle+1)*modulus, highReference); d < bestDistance {
		bestCycle = cycle + 1
	}
	return bestCycle * modulus
}

func distance(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// MediaDuration converts RTP timestamp ticks to the common nanosecond clock.
func (t *Timeline) MediaDuration(extTS uint64) time.Duration {
	return time.Duration(extTS) * time.Second / time.Duration(t.SampleRate)
}

// Observe records a packet and returns its unified position.
func (t *Timeline) Observe(ssrc uint32, seq uint16, ts uint32, arrival time.Time) Point {
	extSeq := t.ExtendSequence(ssrc, seq)
	extTS := t.ExtendTimestamp(ssrc, ts)
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(ssrc)
	if !st.hasOriginTS {
		st.originExtTS = extTS
		st.hasOriginTS = true
	}
	if t.baseArrival.IsZero() || t.baseArrival.UnixNano() == 0 {
		if !arrival.IsZero() {
			t.baseArrival = arrival
		}
	}
	source := t.clockSource[ssrc]
	if source == "" {
		source = "receive_fallback"
		t.clockSource[ssrc] = source
	}
	p := Point{
		SSRC: ssrc, Sequence: seq, ExtSequence: extSeq,
		Timestamp: ts, ExtTS: extTS,
		MediaTime:   t.MediaDuration(extTS - st.originExtTS),
		Arrival:     arrival,
		ClockSource: source,
	}
	if c, ok := t.reports[ssrc]; ok && !c.report.NTPTime.IsZero() {
		deltaSamples := int64(extTS) - int64(c.extRTPTS)
		p.WallTime = c.report.NTPTime.Add(time.Duration(deltaSamples) * time.Second / time.Duration(t.SampleRate))
		p.ClockSource = "sender_report"
	} else if !arrival.IsZero() {
		p.WallTime = arrival
	}
	return p
}

// ApplySenderReport corrects the RTP-to-wall-clock mapping. A backwards NTP
// report is retained as a locatable issue but does not replace the prior good
// correction.
func (t *Timeline) ApplySenderReport(r SenderReport, receivedAt time.Time) *Error {
	extTS := t.ExtendTimestamp(r.SSRC, r.RTPTime)
	t.mu.Lock()
	defer t.mu.Unlock()
	loc := &Location{SSRC: r.SSRC, Timestamp: r.RTPTime, ExtendedSequence: extTS, ByteOffset: r.SourceOffset}
	if old, ok := t.reports[r.SSRC]; ok && !r.NTPTime.IsZero() && r.NTPTime.Before(old.report.NTPTime) {
		err := NewError(CodeClockRollback, "RTCP sender report NTP time moved backwards", loc, nil)
		t.issues = append(t.issues, err)
		return err
	}
	t.reports[r.SSRC] = correction{report: r, extRTPTS: extTS, receivedAt: receivedAt}
	t.clockSource[r.SSRC] = "sender_report"
	return nil
}

// RequireSenderReport records the mandated fallback error if no RTCP report
// has ever been applied for the SSRC.
func (t *Timeline) RequireSenderReport(ssrc uint32, seq uint16, offset int64) *Error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.reports[ssrc]; ok {
		return nil
	}
	t.clockSource[ssrc] = "receive_fallback"
	err := Errorf(CodeMissingClockReport, &Location{SSRC: ssrc, Sequence: seq, ByteOffset: offset},
		"no RTCP sender report; using packet receive time")
	t.issues = append(t.issues, err)
	return err
}

func (t *Timeline) NoteIssue(err *Error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.issues = append(t.issues, err)
}

func (t *Timeline) Issues() []*Error {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*Error, len(t.issues))
	copy(out, t.issues)
	return out
}

func (t *Timeline) ClockSource(ssrc uint32) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.clockSource[ssrc]
}
