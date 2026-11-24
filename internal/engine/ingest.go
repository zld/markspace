// Package engine wires parsing, timeline correction, audio normalization,
// caching, checkpoints and provenance into one deterministic processing run.
package engine

import (
	"encoding/binary"
	"fmt"

	"mediarun/internal/clock"
	rtpx "mediarun/internal/rtp"
	ts "mediarun/internal/ts"

	"github.com/pion/rtp"
)

// Segment is one reassembled, timeline-aligned input unit (one WAV body).
type Segment struct {
	Index       int
	Bytes       []byte
	SourceStart int64
	SourceEnd   int64
	InputDigest string
	FirstPCRMS  int64
}

// IngestReport summarizes transport-level recovery for a run.
type IngestReport struct {
	Packets          int
	SyncErrors       int
	CCGaps           int
	CCDups           int
	Truncations      int
	RecoveredPackets int
	Lost             int
	Late             int
	Duplicates       int
	LossRecoveryRate float64
	PSIEpochs        int
	TimelineMaxMS    int64
	Events           []clock.Event
	Segments         []Segment
}

// frameHeader mirrors the documented fixture framing: marker | seg(2) | off(4).
const (
	frameMarker byte = 0xA5
	frameHdr         = 7
)

type segBuf struct {
	buf        []byte
	start, end int64
	pcrMS      int64
	hasPCR     bool
}

// IngestTS parses a raw MPEG-TS byte stream and reassembles audio segments.
func IngestTS(data []byte, audioPID, videoPID uint16) (*IngestReport, error) {
	rec := &clock.Recorder{}
	raw := ts.SplitStream(data, rec)
	rep := buildPipeline(raw, rec, audioPID, videoPID, false, nil)
	return rep, nil
}

// IngestRTP reorders an RTP transport, then parses the recovered TS packets.
// No media is invented: dropped sequence numbers simply never appear. Every
// transport anomaly (duplicate/late/loss) is also recorded on the shared
// Recorder so all module errors share one time base and event model.
func IngestRTP(packets []*rtp.Packet, window uint16, audioPID, videoPID uint16) (*IngestReport, error) {
	rec := &clock.Recorder{}
	buf := rtpx.NewReorderBuffer(window)
	var ordered []*ts.Packet
	index := 0
	prev := rtpx.Stats{}
	for _, p := range packets {
		ready, duplicate := buf.Feed(p)
		cur := buf.Stats()
		off := int64(p.SequenceNumber) * int64(ts.PacketSize)
		rng := clock.ByteRange{Start: off, End: off + ts.PacketSize}
		if duplicate {
			rec.Add(clock.Event{Kind: "rtp_dup", Severity: "info", Track: "rtp",
				Range: rng, Detail: fmt.Sprintf("duplicate RTP seq=%d discarded", p.SequenceNumber)})
		}
		if cur.Late > prev.Late {
			rec.Add(clock.Event{Kind: "late", Severity: "warning", Track: "rtp",
				Range: rng, Detail: fmt.Sprintf("late RTP seq=%d outside window", p.SequenceNumber)})
		}
		if cur.Lost > prev.Lost {
			rec.Add(clock.Event{Kind: "loss", Severity: "warning", Track: "rtp",
				Range: rng, Detail: fmt.Sprintf("RTP window declared %d lost slot(s)", cur.Lost-prev.Lost)})
		}
		prev = cur
		for _, rp := range ready {
			pkt, err := ts.ParsePacket(rp.Payload, index, int64(index)*int64(ts.PacketSize))
			if err != nil {
				rec.Add(clock.Event{Kind: "sync_error", Severity: "error",
					Range:  clock.ByteRange{Start: int64(index) * 188, End: int64(index+1) * 188},
					Detail: err.Error()})
			} else {
				ordered = append(ordered, pkt)
			}
			index++
		}
	}
	flushed := buf.Flush()
	if cur := buf.Stats(); cur.Lost > prev.Lost {
		rec.Add(clock.Event{Kind: "loss", Severity: "warning", Track: "rtp",
			Range:  clock.ByteRange{Start: -1, End: -1},
			Detail: fmt.Sprintf("end-of-stream flush declared %d lost slot(s)", cur.Lost-prev.Lost)})
	}
	for _, rp := range flushed {
		pkt, err := ts.ParsePacket(rp.Payload, index, int64(index)*int64(ts.PacketSize))
		if err == nil {
			ordered = append(ordered, pkt)
		}
		index++
	}
	stats := buf.Stats()
	rep := buildPipeline(ordered, rec, audioPID, videoPID, true, &stats)
	return rep, nil
}

// buildPipeline runs PSI mapping, continuity checks, PCR/PTS timeline and
// audio-frame reassembly over an ordered packet list.
func buildPipeline(packets []*ts.Packet, rec *clock.Recorder, audioPID, videoPID uint16,
	fromRTP bool, rtpStats *rtpx.Stats) *IngestReport {
	psi := ts.NewPSITracker()
	checker := ts.NewContinuityChecker(map[uint16]string{audioPID: "audio", videoPID: "video"})
	tl := clock.NewTimeline()
	segs := map[int]*segBuf{}
	var segOrder []int

	for _, p := range packets {
		psi.Observe(p, rec)
		isDup := checker.Observe(p, rec)
		if isDup {
			continue // duplicates are ignored, never reprocessed
		}
		var pcrMS int64 = -1
		if p.HasAdaptation && p.Adaptation.HasPCR {
			ms, _, ambiguous := tl.FeedPCR(p.Adaptation.PCRBase)
			if ambiguous {
				rec.Add(clock.Event{Kind: "ambiguous_ts", Severity: "error", PID: p.PID,
					Track:  psi.TrackOf(p.PID),
					Range:  clock.ByteRange{Start: p.Offset, End: p.Offset + 188},
					Detail: fmt.Sprintf("undecidable PCR on pid 0x%x", p.PID)})
			} else {
				pcrMS = ms
			}
		}
		// PTS unwrapping uses the same shared clock; undecidable jumps become
		// error events carrying the owning PID and affected byte range.
		if rawPTS, hasPTS := p.ExtractPTS(); hasPTS {
			if _, _, ambiguous := tl.FeedPTS(p.PID, rawPTS); ambiguous {
				rec.Add(clock.Event{Kind: "ambiguous_ts", Severity: "error", PID: p.PID,
					Track:  psi.TrackOf(p.PID),
					Range:  clock.ByteRange{Start: p.Offset, End: p.Offset + 188},
					Detail: fmt.Sprintf("undecidable PTS on pid 0x%x", p.PID)})
			}
		}
		if p.PID == audioPID && p.HasPayload && len(p.Payload) >= frameHdr && p.Payload[0] == frameMarker {
			absorbFrame(segs, &segOrder, p, pcrMS)
		}
	}

	counts := rec.CountByKind()
	rep := &IngestReport{
		Packets:     len(packets),
		SyncErrors:  counts["sync_error"] + counts["adaptation_error"],
		CCGaps:      counts["cc_gap"],
		CCDups:      counts["cc_dup"],
		Truncations: counts["truncated"],
		PSIEpochs:   psi.Epoch(),
		Events:      rec.Events(),
		// A non-RTP transport has no packet-loss concept; report perfect health.
		LossRecoveryRate: 1.0,
	}
	if fromRTP && rtpStats != nil {
		rep.RecoveredPackets = rtpStats.Recovered
		rep.Lost = rtpStats.Lost
		rep.Late = rtpStats.Late
		rep.Duplicates = rtpStats.Duplicates
		rep.LossRecoveryRate = lossRate(rtpStats)
	}
	rep.Segments = finalizeSegments(segs, segOrder)
	rep.TimelineMaxMS = timelineSpan(rep.Segments)
	return rep
}

func lossRate(s *rtpx.Stats) float64 {
	denom := s.Recovered + s.Lost
	if denom == 0 {
		return 1
	}
	return float64(s.Recovered) / float64(denom)
}

// absorbFrame places one framed chunk into its segment buffer at its offset.
func absorbFrame(segs map[int]*segBuf, order *[]int, p *ts.Packet, pcrMS int64) {
	pl := p.Payload
	seg := int(binary.BigEndian.Uint16(pl[1:3]))
	off := int(binary.BigEndian.Uint32(pl[3:7]))
	chunk := pl[frameHdr:]
	sb, ok := segs[seg]
	if !ok {
		sb = &segBuf{start: p.Offset, pcrMS: pcrMS, hasPCR: pcrMS >= 0}
		segs[seg] = sb
		*order = append(*order, seg)
	}
	if off+len(chunk) > len(sb.buf) {
		grown := make([]byte, off+len(chunk))
		copy(grown, sb.buf)
		sb.buf = grown
	}
	copy(sb.buf[off:], chunk)
	if p.Offset < sb.start {
		sb.start = p.Offset
	}
	if end := p.Offset + int64(ts.PacketSize); end > sb.end {
		sb.end = end
	}
	if !sb.hasPCR && pcrMS >= 0 {
		sb.pcrMS, sb.hasPCR = pcrMS, true
	}
}

func finalizeSegments(segs map[int]*segBuf, order []int) []Segment {
	out := make([]Segment, 0, len(order))
	for _, idx := range order {
		sb := segs[idx]
		out = append(out, Segment{
			Index:       idx,
			Bytes:       append([]byte(nil), sb.buf...),
			SourceStart: sb.start,
			SourceEnd:   sb.end,
			InputDigest: digestBytes(sb.buf),
			FirstPCRMS:  sb.pcrMS,
		})
	}
	return out
}

func timelineSpan(segs []Segment) int64 {
	var max int64
	for _, s := range segs {
		if s.FirstPCRMS > max {
			max = s.FirstPCRMS
		}
	}
	return max
}
