// Package inspect reassembles H.264 NAL units from the committed RTP
// stream, classifies them (SPS/PPS/IDR/slice), measures keyframe intervals
// and localizes corrupt regions (packet loss, truncated NALs, missing
// fragments) to media-time ranges.
package inspect

import (
	"fmt"

	"github.com/pion/rtp"

	"mediadag/internal/clock"
	"mediadag/internal/ingest"
)

// H.264 NAL unit types relevant to stream structure analysis.
const (
	nalSlice = 1
	nalIDR   = 5
	nalSPS   = 7
	nalPPS   = 8
	nalSTAPA = 24
	nalFUA   = 28
)

// CorruptRange localizes a defect to a media-time interval in milliseconds.
type CorruptRange struct {
	StartMs float64
	EndMs   float64
	Reason  string // "packet-loss" | "truncated-nal" | "missing-fragment"
}

// Frame describes one reconstructed access unit on the media timeline.
type Frame struct {
	TimeMs float64
	Bytes  int
	IDR    bool
}

// Analysis is the structural summary of the inspected bitstream.
type Analysis struct {
	SPSCount      int
	PPSCount      int
	IDRCount      int
	SliceCount    int
	KeyframeIntMs []float64 // intervals between consecutive IDR frames
	Corrupt       []CorruptRange
	Frames        int
	FrameList     []Frame
	MediaBytes    int
	TimeUncertain bool // true when no clock anchor was available
	DurationMs    float64
}

// Analyzer consumes committed RTP events in order.
type Analyzer struct {
	tl *clock.Timeline

	// FU-A reassembly state
	fuActive bool
	fuTS     uint32
	fuType   uint8

	// per-frame NAL classification, keyed by RTP timestamp
	frameNALs  map[uint32][]uint8
	frameOrd   []uint32
	frameBytes map[uint32]int

	corrupt []CorruptRange
	bytes   int
}

// NewAnalyzer creates an Analyzer bound to the shared timeline.
func NewAnalyzer(tl *clock.Timeline) *Analyzer {
	return &Analyzer{tl: tl, frameNALs: make(map[uint32][]uint8), frameBytes: make(map[uint32]int)}
}

func (a *Analyzer) ms(ts uint32) float64 {
	ms, ok := a.tl.MediaMs(a.tl.Extend(ts))
	if !ok {
		// fall back to ticks so ranges remain orderable
		return float64(ts)
	}
	return ms
}

func (a *Analyzer) addCorrupt(ts uint32, reason string) {
	ms := a.ms(ts)
	a.corrupt = append(a.corrupt, CorruptRange{StartMs: ms, EndMs: ms, Reason: reason})
}

// Add processes one committed RTP event.
func (a *Analyzer) Add(ev ingest.Event) {
	if ev.Kind != ingest.KindRTP || ev.Packet == nil {
		return
	}
	p := ev.Packet
	a.bytes += len(p.Payload)
	a.frameBytes[p.Timestamp] += len(p.Payload)
	if len(p.Payload) == 0 {
		a.addCorrupt(p.Timestamp, "truncated-nal")
		return
	}
	nalType := p.Payload[0] & 0x1F
	switch nalType {
	case nalSTAPA:
		a.addSTAPA(p)
	case nalFUA:
		a.addFUA(p)
	default:
		if a.fuActive && p.Timestamp != a.fuTS {
			a.abortFU("truncated-nal")
		}
		a.recordNAL(p.Timestamp, nalType)
	}
}

// addSTAPA parses an aggregation packet: 1-byte header, then repeated
// 2-byte big-endian size + NAL unit.
func (a *Analyzer) addSTAPA(p *rtp.Packet) {
	if a.fuActive && p.Timestamp != a.fuTS {
		a.abortFU("truncated-nal")
	}
	body := p.Payload[1:]
	for len(body) >= 2 {
		size := int(body[0])<<8 | int(body[1])
		body = body[2:]
		if size == 0 || size > len(body) {
			a.addCorrupt(p.Timestamp, "truncated-nal")
			return
		}
		a.recordNAL(p.Timestamp, body[0]&0x1F)
		body = body[size:]
	}
	if len(body) != 0 {
		a.addCorrupt(p.Timestamp, "truncated-nal")
	}
}

// addFUA processes one fragmentation unit. A start fragment opens the
// reassembly; a missing end before the next timestamp is a truncated NAL.
func (a *Analyzer) addFUA(p *rtp.Packet) {
	if len(p.Payload) < 2 {
		a.addCorrupt(p.Timestamp, "truncated-nal")
		return
	}
	fuHeader := p.Payload[1]
	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	nalType := fuHeader & 0x1F
	if start {
		if a.fuActive {
			a.abortFU("missing-fragment")
		}
		a.fuActive = true
		a.fuTS = p.Timestamp
		a.fuType = nalType
		return
	}
	if !a.fuActive || p.Timestamp != a.fuTS {
		// continuation/end without a start: fragment(s) lost
		a.fuActive = false
		a.addCorrupt(p.Timestamp, "missing-fragment")
		return
	}
	if end {
		a.recordNAL(a.fuTS, a.fuType)
		a.fuActive = false
	}
}

func (a *Analyzer) recordNAL(ts uint32, nalType uint8) {
	if _, ok := a.frameNALs[ts]; !ok {
		a.frameOrd = append(a.frameOrd, ts)
	}
	a.frameNALs[ts] = append(a.frameNALs[ts], nalType)
}

func (a *Analyzer) abortFU(reason string) {
	a.addCorrupt(a.fuTS, reason)
	a.fuActive = false
}

// AddLossGaps converts committed sequence gaps into corrupt ranges located
// at the timestamp of the first packet after each gap.
func (a *Analyzer) AddLossGaps(gaps []ingest.Gap, afterTS func(g ingest.Gap) (uint32, bool)) {
	for _, g := range gaps {
		if ts, ok := afterTS(g); ok {
			a.addCorrupt(ts, "packet-loss")
		}
	}
}

// Finish closes any dangling FU and computes the final analysis.
func (a *Analyzer) Finish() *Analysis {
	if a.fuActive {
		a.abortFU("truncated-nal")
	}
	res := &Analysis{
		Corrupt:    a.corrupt,
		MediaBytes: a.bytes,
	}
	var idrTimes []float64
	for _, ts := range a.frameOrd {
		res.Frames++
		fr := Frame{TimeMs: a.ms(ts), Bytes: a.frameBytes[ts]}
		for _, nt := range a.frameNALs[ts] {
			switch nt {
			case nalSPS:
				res.SPSCount++
			case nalPPS:
				res.PPSCount++
			case nalIDR:
				res.IDRCount++
				fr.IDR = true
				idrTimes = append(idrTimes, fr.TimeMs)
			case nalSlice:
				res.SliceCount++
			}
		}
		res.FrameList = append(res.FrameList, fr)
	}
	for i := 1; i < len(idrTimes); i++ {
		res.KeyframeIntMs = append(res.KeyframeIntMs, idrTimes[i]-idrTimes[i-1])
	}
	if len(a.frameOrd) >= 2 {
		first, last := a.ms(a.frameOrd[0]), a.ms(a.frameOrd[len(a.frameOrd)-1])
		res.DurationMs = last - first
	}
	res.TimeUncertain = !a.tl.Anchored()
	return res
}

// Summary renders a one-line human-readable digest.
func (a *Analysis) Summary() string {
	return fmt.Sprintf("frames=%d sps=%d pps=%d idr=%d slices=%d corrupt=%d",
		a.Frames, a.SPSCount, a.PPSCount, a.IDRCount, a.SliceCount, len(a.Corrupt))
}
