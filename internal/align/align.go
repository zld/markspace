// Package align places client events on the media timeline and derives stall
// intervals from the reconstructed packet timeline.
//
// Every event keeps its original record. An event whose timestamp cannot be
// converted, or whose conversion overflows, is marked unalignable with a reason
// instead of being dropped or aborting the pass.
//
// Stall intervals use the fixed 90 kHz clock: two consecutive valid packets
// whose timestamps differ by more than ThresholdTicks (18000 ticks, 200 ms) are
// an anomaly. Each interval records both packet sequence numbers, both times
// and any events that fall inside it.
package align

import (
	"fmt"
	"sort"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/events"
	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/rtp"
)

// ThresholdTicks is the fixed stall threshold: 200 ms at 90 kHz.
const ThresholdTicks clock.Tick = clock.GapThresholdTicks

// Event is one event placed (or not) on the media timeline.
type Event struct {
	Record events.Event
	Media  clock.Tick
	// InInterval is the index of the stall interval containing this event, or
	// -1. -1 with Alignable set means the event fell outside every interval.
	Interval int
}

// Interval is one stall interval.
type Interval struct {
	StartSeq   uint16
	EndSeq     uint16
	StartExt   int64
	EndExt     int64
	StartTime  clock.Tick
	EndTime    clock.Tick
	Gap        clock.Tick
	Events     []int // indices into Result.Events
	SSRC       uint32
	StartInput int64
	EndInput   int64
}

// Config controls alignment.
type Config struct {
	// CaptureStartMillis is the wall clock of media time zero. HasCaptureStart
	// distinguishes "unknown" from a legitimate zero.
	CaptureStartMillis int64
	HasCaptureStart    bool
	// MediaOrigin is subtracted from packet timestamps so media time starts at
	// zero. It is the first emitted packet's unwrapped timestamp.
	MediaOrigin clock.Tick
	// CaptureOrigin is used when MediaOrigin is unset, for callers that know
	// the sender's initial timestamp without holding the packets.
	CaptureOrigin clock.Tick
	// ThresholdTicks overrides the stall threshold; 0 uses ThresholdTicks.
	ThresholdTicks clock.Tick
}

// Result is the alignment output.
type Result struct {
	Events      []Event
	Intervals   []Interval
	Unalignable int
	// ClockOffsetKnown reports whether the capture start was known. When it is
	// not, absolute wall clock events are explicitly marked unknown.
	ClockOffsetKnown bool
	Threshold        clock.Tick
}

// Run aligns events against the given ordered packets.
func Run(packets []rtp.Packet, log *events.Log, cfg Config) (*Result, error) {
	threshold := cfg.ThresholdTicks
	if threshold <= 0 {
		threshold = ThresholdTicks
	}
	// Packet time is rebased to media zero so relative event offsets and packet
	// times share one origin. An explicit origin wins; otherwise the first
	// packet defines it, matching the unwrapped sender timeline.
	origin := cfg.MediaOrigin
	if origin == 0 {
		origin = cfg.CaptureOrigin
	}
	if len(packets) > 0 && origin == 0 {
		origin = packets[0].TS
	}
	packets = rebase(packets, origin)
	res := &Result{Threshold: threshold, ClockOffsetKnown: cfg.HasCaptureStart}

	for _, ev := range log.Events {
		placed, err := place(ev, cfg)
		if err != nil {
			placed.Record = ev
			placed.Record.Alignable = false
			placed.Record.Reason = err.Error()
			placed.Interval = -1
			res.Unalignable++
			res.Events = append(res.Events, placed)
			continue
		}
		placed.Interval = -1
		res.Events = append(res.Events, placed)
	}

	res.Intervals = intervals(packets, threshold)
	for i := range res.Events {
		if !res.Events[i].Record.Alignable {
			continue
		}
		for j := range res.Intervals {
			iv := &res.Intervals[j]
			if res.Events[i].Media >= iv.StartTime && res.Events[i].Media <= iv.EndTime {
				iv.Events = append(iv.Events, i)
				res.Events[i].Interval = j
				break
			}
		}
	}
	return res, nil
}

func place(ev events.Event, cfg Config) (Event, error) {
	out := Event{Record: ev}
	switch {
	case ev.HasRelative:
		out.Media = ev.Relative
	case ev.WallMillis != 0:
		if !cfg.HasCaptureStart {
			return out, fmt.Errorf("%w: wall clock event without a capture start", ErrUnknownOffset)
		}
		tick, err := events.MediaFromWall(ev.WallMillis, cfg.CaptureStartMillis)
		if err != nil {
			return out, err
		}
		out.Media = tick
	default:
		return out, fmt.Errorf("%w: event has no usable timestamp", ErrNoTimestamp)
	}
	if err := out.Media.Validate(); err != nil {
		return out, err
	}
	out.Record.Alignable = true
	out.Record.MediaTime = out.Media
	out.Record.Reason = ""
	return out, nil
}

// rebase returns packets with timestamps relative to origin. Packets whose
// timestamp is suspect keep their (meaningless) value but stay excluded from
// interval analysis.
func rebase(packets []rtp.Packet, origin clock.Tick) []rtp.Packet {
	if origin == 0 || len(packets) == 0 {
		return packets
	}
	out := make([]rtp.Packet, len(packets))
	copy(out, packets)
	for i := range out {
		if out[i].TSSuspect {
			continue
		}
		out[i].TS -= origin
	}
	return out
}

// ErrUnknownOffset marks events that cannot be tied to the media clock because
// the cross-host offset is not present in the material.
var ErrUnknownOffset = fmt.Errorf("%w: clock offset unknown", fault.ErrUnsupported)

// ErrNoTimestamp marks events without a timestamp.
var ErrNoTimestamp = fmt.Errorf("%w: missing event timestamp", fault.ErrCorrupt)

// intervals builds stall intervals from consecutive packet timestamps.
//
// Only packets with a valid timeline stamp take part; a packet whose timestamp
// overflowed or that was unplaceable never appears here because the reorderer
// omits it. Non-positive deltas are reordering noise below the threshold and are
// not intervals.
func intervals(packets []rtp.Packet, threshold clock.Tick) []Interval {
	var out []Interval
	for i := 1; i < len(packets); i++ {
		prev, cur := packets[i-1], packets[i]
		if prev.TSSuspect || cur.TSSuspect {
			continue
		}
		gap := cur.TS - prev.TS
		if gap <= threshold {
			continue
		}
		out = append(out, Interval{
			SSRC:       cur.SSRC,
			StartSeq:   prev.Seq,
			EndSeq:     cur.Seq,
			StartExt:   prev.ExtSeq,
			EndExt:     cur.ExtSeq,
			StartTime:  prev.TS,
			EndTime:    cur.TS,
			Gap:        gap,
			StartInput: prev.InputOff,
			EndInput:   cur.InputOff,
		})
	}
	return out
}

// SortIntervals orders intervals by media time.
func SortIntervals(iv []Interval) {
	sort.SliceStable(iv, func(i, j int) bool { return iv[i].StartTime < iv[j].StartTime })
}

// AnomalyIntervals returns all intervals with a gap above the threshold.
func (r *Result) AnomalyIntervals() int { return len(r.Intervals) }
