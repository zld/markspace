// Package pipeline wires reordering, the unified clock, RTCP anchoring
// and stream scanning into one deterministic processing path. Its output
// is the data every other layer (manifest, cache, index, CLI report) is
// built from, expressed only in unified coordinates.
package pipeline

import (
	rtppacket "github.com/pion/rtp"

	"mediacheck/internal/clock"
	"mediacheck/internal/fault"
	"mediacheck/internal/rtp"
	"mediacheck/internal/stream"
)

// Datagram is one transport datagram handed to the pipeline.
type Datagram struct {
	Raw    []byte
	IsRTCP bool
}

// SampleOutput is one row of the per-sample report.
type SampleOutput struct {
	Index      int
	Codec      string
	KeyFrame   bool
	FirstSeq   uint64
	LastSeq    uint64
	ByteOffset int64
	ByteLength int64
	TimeStart  int64
	TimeEnd    int64
	Duration   int64
	Faults     []string
	NALUnits   [][]byte
}

// Report is the complete result of processing one track.
type Report struct {
	Codec        string
	SSRC         uint32
	ClockRate    uint32
	TimeTrusted  bool
	Samples      []SampleOutput
	Faults       []fault.Fault
	ReorderStats rtp.Stats
	InputCount   int
}

// clockTrack pairs a unified timeline with the origin normalization
// applied so the first observed timestamp maps to media tick zero.
type clockTrack struct {
	tl     *clock.Timeline
	origin int64
	seeded bool
}

func newClockTrack(rate uint32) *clockTrack {
	return &clockTrack{tl: clock.NewTimeline(rate)}
}

// feed expands a raw RTP timestamp and returns the zero-based tick.
func (c *clockTrack) feed(raw uint32) int64 {
	exp := c.tl.Feed(raw)
	if !c.seeded {
		c.seeded = true
		c.origin = exp
	}
	return exp - c.origin
}

func (c *clockTrack) bind(a clock.SenderAnchor) { c.tl.BindAnchor(a) }
func (c *clockTrack) trusted() bool             { return c.tl.Trusted() }

// Process runs the full pipeline over an arrival-order datagram list.
// reorderWindow bounds the reorder buffer; clockRate anchors the track.
func Process(codec stream.Codec, ssrc uint32, clockRate uint32, reorderWindow int, datagrams []Datagram) *Report {
	rep := &Report{Codec: string(codec), SSRC: ssrc, ClockRate: clockRate}
	buf := rtp.NewReorderBuffer(reorderWindow)
	clk := newClockTrack(clockRate)
	scanner := stream.NewScanner(codec)
	rec := fault.NewRecorder()
	// placeholders are expanded seqs occupied by header-damaged datagrams:
	// they keep reorder accounting correct but never enter the scanner.
	placeholders := map[uint64]bool{}

	handle := func(res rtp.PushResult) {
		recordGaps(res, rec, codec)
		for _, op := range res.Delivered {
			if placeholders[op.ExpandedSeq] {
				delete(placeholders, op.ExpandedSeq)
				continue
			}
			op.Packet.Timestamp = uint32(clk.feed(op.Packet.Timestamp))
			scanner.Push(op)
		}
	}

	// pushPlaceholder accounts for a header-damaged datagram in the
	// reorder buffer using its raw sequence number.
	pushPlaceholder := func(b *rtp.ReorderBuffer, raw []byte) rtp.PushResult {
		seq := uint16(0)
		if len(raw) >= 4 {
			seq = uint16(raw[2])<<8 | uint16(raw[3])
		}
		expanded := b.ExpandSeq(seq)
		placeholders[expanded] = true
		ph := &rtppacket.Packet{
			Header: rtppacket.Header{
				Version: 2, PayloadType: 96, SSRC: ssrc, SequenceNumber: seq,
			},
		}
		return b.Push(ph)
	}

	for _, d := range datagrams {
		rep.InputCount++
		if d.IsRTCP {
			applyRTCP(clk, d.Raw, rec, codec)
			continue
		}
		pkt, err := rtp.DecodeDatagram(d.Raw)
		if err != nil {
			// The damaged datagram still occupies its sequence number:
			// a placeholder keeps reorder accounting correct, while the
			// fault is reported exactly once as a truncated header and the
			// placeholder itself never reaches the media scanner.
			rawSeq := uint16(0)
			if len(d.Raw) >= 4 {
				rawSeq = uint16(d.Raw[2])<<8 | uint16(d.Raw[3])
			}
			expanded := buf.ExpandSeq(rawSeq)
			res := pushPlaceholder(buf, d.Raw)
			rec.Add(fault.Fault{
				Class:  fault.ClassTruncatedHead,
				Codec:  string(codec),
				Seq:    fault.SeqRange{First: expanded, Last: expanded},
				Detail: err.Error(),
			})
			recordGaps(res, rec, codec)
			continue
		}
		handle(buf.Push(pkt))
	}
	handle(buf.Flush())
	rep.ReorderStats = buf.Stats()

	scan := scanner.Flush()
	rep.Faults = mergeFaults(rec.All(), scan.Faults)
	rep.TimeTrusted = clk.trusted()
	rep.buildSamples(scan)
	return rep
}

func gapDetail(g rtp.Gap) string {
	if g.OverWindow {
		return "seq aged out of bounded reorder window"
	}
	return "seq never observed before stream end"
}

// recordGaps turns reorder gaps into localized missing-packet faults.
func recordGaps(res rtp.PushResult, rec *fault.Recorder, codec stream.Codec) {
	for _, g := range res.Gaps {
		rec.Add(fault.Fault{
			Class:      fault.ClassMissingPacket,
			Codec:      string(codec),
			Seq:        g.Seq,
			OverWindow: g.OverWindow,
			Detail:     gapDetail(g),
		})
	}
}

func applyRTCP(clk *clockTrack, raw []byte, rec *fault.Recorder, codec stream.Codec) {
	reports, err := rtp.ParseCompoundRTCP(raw)
	if err != nil {
		rec.Add(fault.Fault{Class: fault.ClassMalformedRTCP, Codec: string(codec), Detail: err.Error()})
		return
	}
	for _, sr := range reports {
		clk.bind(clock.SenderAnchor{RTPTimestamp: sr.RTPTimestamp, NTPSeconds: sr.NTPSeconds})
	}
}

// mergeFaults de-duplicates exact faults both layers observed and keeps
// recorder faults ahead of scan-level findings for stable ordering.
func mergeFaults(a, b []fault.Fault) []fault.Fault {
	out := make([]fault.Fault, 0, len(a)+len(b))
	seen := map[string]bool{}
	key := func(f fault.Fault) string {
		return string(f.Class) + "|" + f.Detail + "|" +
			itoa(f.Seq.First) + "-" + itoa(f.Seq.Last) + "|" + itoa(uint64(f.Byte.Offset))
	}
	for _, f := range append(append([]fault.Fault{}, a...), b...) {
		if k := key(f); seen[k] {
			continue
		} else {
			seen[k] = true
		}
		out = append(out, f)
	}
	return out
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func extractNALData(nals []stream.NALUnit) [][]byte {
	out := make([][]byte, 0, len(nals))
	for _, nal := range nals {
		cp := make([]byte, len(nal.Data))
		copy(cp, nal.Data)
		out = append(out, cp)
	}
	return out
}

func (r *Report) buildSamples(scan stream.ScanResult) {
	faultsBySeq := map[uint64][]string{}
	for _, f := range r.Faults {
		for q := f.Seq.First; q <= f.Seq.Last; q++ {
			faultsBySeq[q] = append(faultsBySeq[q], string(f.Class))
		}
	}
	r.Samples = make([]SampleOutput, 0, len(scan.Samples))
	for _, s := range scan.Samples {
		classes := map[string]bool{}
		for q := s.FirstSeq; q <= s.LastSeq; q++ {
			for _, c := range faultsBySeq[q] {
				classes[c] = true
			}
		}
		row := SampleOutput{
			Index:      s.Index,
			Codec:      string(s.Codec),
			KeyFrame:   s.KeyFrame,
			FirstSeq:   s.FirstSeq,
			LastSeq:    s.LastSeq,
			ByteOffset: s.Byte.Offset,
			ByteLength: s.Byte.Length,
			TimeStart:  s.Time.Start,
			TimeEnd:    s.Time.End,
			Duration:   s.Duration,
			NALUnits:   extractNALData(s.Contributed),
		}
		for c := range classes {
			row.Faults = append(row.Faults, c)
		}
		r.Samples = append(r.Samples, row)
	}
}
