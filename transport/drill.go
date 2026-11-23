package transport

import (
	"fmt"
	"time"
)

// DrillStep is one scripted delivery: the sequence number to transmit and how
// many times to repeat it (repeats after the first are duplicates).
type DrillStep struct {
	Seq     uint16
	Repeats int
}

// RTPDrill is a fully deterministic receive scenario with expected counters.
type RTPDrill struct {
	Datagrams [][]byte
	Expected  Stats
	Gaps      []Gap
	Schedule  []DrillStep
}

// DefaultRTPDrill builds the demo receive stream: in-order packets, one burst
// gap that exceeds the reorder window (so it is declared lost, not recovered),
// five packets that arrive behind the high-water mark but in time to be
// reordered into place, and three duplicates. The schedule is fixed so the
// counters are reproducible.
func DefaultRTPDrill(seed int64) (*RTPDrill, error) {
	const (
		ssrc      = 0x4d454449 // "MEDI"
		payloadT  = 96
		window    = 4
		timestamp = 90000
	)
	_ = seed // schedule is fixed; seed is retained for API symmetry
	// Sequence 10 and 11 are never transmitted: a 2-packet burst loss.
	schedule := []DrillStep{
		{0, 1}, {1, 1}, {2, 1}, {3, 1}, {4, 1}, {5, 1}, {6, 1}, {7, 1}, {8, 1}, {9, 1},
		{13, 1}, {14, 1}, {15, 1},
		{12, 1}, // arrives behind the high-water mark: reordered into place
		{16, 1}, {17, 1}, {18, 1},
		{5, 1}, // duplicate
		{21, 1}, {22, 1},
		{20, 1}, {19, 1}, // two late arrivals
		{24, 1},
		{23, 1}, // late arrival
		{26, 1},
		{25, 1}, // late arrival
		{16, 1}, // duplicate
		{24, 1}, // duplicate
	}
	d := &RTPDrill{Schedule: schedule}
	rec, err := NewRecorder(ssrc, window, 64)
	if err != nil {
		return nil, err
	}
	base := time.Unix(1700000000, 0)
	stepN := 0
	for _, st := range schedule {
		payload := make([]byte, 120)
		for i := range payload {
			payload[i] = byte(int(st.Seq)*7 + i)
		}
		datagram, err := BuildPacket(ssrc, st.Seq, uint32(st.Seq)*timestamp/25, payloadT, payload)
		if err != nil {
			return nil, err
		}
		d.Datagrams = append(d.Datagrams, datagram)
		arrival := base.Add(time.Duration(stepN) * time.Millisecond).UnixNano()
		if _, err := rec.Consume(datagram, arrival); err != nil {
			return nil, err
		}
		stepN++
	}
	rec.Finish()
	d.Expected = rec.Stats()
	d.Gaps = rec.Gaps()
	return d, nil
}

// Replay drives a fresh Recorder over the stored datagrams and returns its
// counters, gaps and the number of packets released in order. It is used by
// the pipeline and by tests to prove the drill is reproducible.
func (d *RTPDrill) Replay(window, dedupWindow int, ssrc uint32) (Stats, []Gap, []Packet, error) {
	rec, err := NewRecorder(ssrc, window, dedupWindow)
	if err != nil {
		return Stats{}, nil, nil, err
	}
	var ordered []Packet
	base := time.Unix(1700000000, 0)
	for i, datagram := range d.Datagrams {
		out, err := rec.Consume(datagram, base.Add(time.Duration(i)*time.Millisecond).UnixNano())
		if err != nil {
			return Stats{}, nil, nil, err
		}
		ordered = append(ordered, out...)
	}
	tail, gaps := rec.Finish()
	ordered = append(ordered, tail...)
	return rec.Stats(), gaps, ordered, nil
}

// FeedController drives a bandwidth controller over the drill's datagrams at a
// varying arrival pace: each entry is one measurement window whose duration
// covers the whole drill. Windows alternate fast and slow so the sliding
// statistics see a genuinely changing rate, and the controller ends on the
// medium pace that the pool inherits.
func (d *RTPDrill) FeedController(ctl *Controller, windows []time.Duration) *Controller {
	base := time.Unix(1700000000, 0)
	total := 0
	for _, dg := range d.Datagrams {
		total += len(dg)
	}
	for i, span := range windows {
		ctl.ObservePackets(base.Add(time.Duration(i)*span), total, len(d.Datagrams))
	}
	return ctl
}

// String summarizes the drill for terminal output.
func (d *RTPDrill) String() string {
	return fmt.Sprintf("rtp drill packets=%d expected{%s}", len(d.Datagrams), d.Expected)
}
