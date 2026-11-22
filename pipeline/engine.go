// Package pipeline chains packet parsing, the tick timeline, bitstream
// scanning and damage reporting into one deterministic analysis.
package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pion/rtp"

	"streamcheck/bitstream"
	"streamcheck/clock"
	"streamcheck/damage"
	"streamcheck/packet"
)

// Options configures one analysis run.
type Options struct {
	Seed        int64
	Scenario    string
	WatermarkMs int64
}

// Report is the full deterministic result of one run.
type Report struct {
	Scenario            string
	Records             int
	ParsedOK            int
	ParseErrors         int
	Packets             int // valid RTP packets accepted into the timeline
	RTCPPackets         int
	Loss                int // lost sequence numbers
	Reordered           int
	Duplicates          int
	Late                int // late events replayed into closed windows
	KeyframeHitRate     float64
	RoundtripRate       float64
	Damage              []damage.Range
	Segments            [][32]byte // per-window timeline hashes, in ID order
	KeyframeTicks       []int64    // tick of each keyframe, in order
	RecoverableBoundary int64      // input offset past the last sound NAL
	HasBoundary         bool
}

// ParseRate returns the fraction of input records parsed successfully.
func (r *Report) ParseRate() float64 {
	if r.Records == 0 {
		return 0
	}
	return float64(r.ParsedOK) / float64(r.Records)
}

// span maps one emitted packet between buffer, input and tick space.
type span struct {
	bufStart, bufEnd int64
	extSeq           int64
	tick             int64
	recStart, recEnd int64
	payloadOff       int64
}

// Run generates the scenario stream and analyzes it twice, requiring
// both runs to produce identical reports.
func Run(opts Options) (*Report, error) {
	if !IsScenario(opts.Scenario) {
		return nil, fmt.Errorf("unknown scenario %q", opts.Scenario)
	}
	data := generate(opts.Seed, opts.Scenario)
	r1, err := analyze(data, opts)
	if err != nil {
		return nil, err
	}
	r2, err := analyze(data, opts)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(r1, r2) {
		return nil, errors.New("invariant: repeated run produced a different report")
	}
	return r1, nil
}

func analyze(data []byte, opts Options) (*Report, error) {
	lag, err := clock.MillisToTicks(opts.WatermarkMs, genClockRate)
	if err != nil {
		return nil, err
	}
	windowSize := max(lag, genTicksPerPacket)
	reo := packet.NewReorderer(packet.ReordererConfig{Window: 16})
	ext := clock.NewExtender(1 << 20)
	wm := clock.NewWatermark(lag)
	replay := clock.NewReplayer(windowSize, 64)

	rep := &Report{Scenario: opts.Scenario}
	var damages []damage.Range
	var buf []byte
	var spans []span
	var losses [][2]int64
	lastTick := int64(-1)
	monotonic := true
	var roundtripOK, rtpRecords int

	handle := func(ev packet.Event) {
		if ev.Kind == packet.KindLoss {
			rep.Loss += int(ev.LossTo - ev.LossFrom + 1)
			losses = append(losses, [2]int64{ev.LossFrom, ev.LossTo})
			return
		}
		info := ev.Info
		tick, terr := ext.Extend(info.Timestamp)
		if terr != nil {
			rep.ParseErrors++
			damages = append(damages, damage.Range{
				ByteStart: info.Offset, ByteEnd: info.Offset + int64(info.RawLen),
				Class: damage.ClassParseError, Detail: terr.Error()})
			return
		}
		if lastTick >= 0 && tick < lastTick {
			monotonic = false
		}
		lastTick = tick
		rep.Packets++
		rec := clock.EventRecord{Time: tick, ExtSeq: ev.ExtSeq, Hash: sha256.Sum256(info.Payload)}
		if wm.Closed(tick) {
			if _, ok := replay.AddLate(rec); ok {
				rep.Late++
			}
		} else {
			replay.Add(rec, wm.Observe(tick))
		}
		s := span{
			bufStart: int64(len(buf)), extSeq: ev.ExtSeq, tick: tick,
			recStart: info.Offset, recEnd: info.Offset + recHdrLen + int64(info.RawLen),
			payloadOff: info.Offset + recHdrLen + int64(info.HeaderLen),
		}
		s.bufEnd = s.bufStart + int64(len(info.Payload))
		buf = append(buf, info.Payload...)
		spans = append(spans, s)
	}

	for off := int64(0); off < int64(len(data)); {
		rep.Records++
		remaining := data[off:]
		if len(remaining) < recHdrLen {
			rep.ParseErrors++
			damages = append(damages, damage.Range{ByteStart: off, ByteEnd: int64(len(data)),
				Class: damage.ClassParseError, Detail: "truncated record header"})
			break
		}
		typ := remaining[0]
		ln := int(binary.BigEndian.Uint16(remaining[1:3]))
		end := min(off+recHdrLen+int64(ln), int64(len(data)))
		raw := data[off+recHdrLen : end]
		switch typ {
		case recTypeRTP:
			info, perr := packet.ParseRTP(raw, off+recHdrLen)
			if perr != nil {
				rep.ParseErrors++
				damages = append(damages, damage.Range{ByteStart: off, ByteEnd: end,
					Class: damage.ClassParseError, Detail: perr.Error()})
				break
			}
			rep.ParsedOK++
			rtpRecords++
			info.Offset = off
			var p rtp.Packet
			if uerr := p.Unmarshal(raw); uerr == nil {
				if b, merr := p.Marshal(); merr == nil && bytes.Equal(b, raw) {
					roundtripOK++
				}
			}
			for _, ev := range reo.Push(*info) {
				handle(ev)
			}
		case recTypeRTCP:
			ri, perr := packet.ParseRTCP(raw, off+recHdrLen)
			if perr != nil {
				rep.ParseErrors++
				damages = append(damages, damage.Range{ByteStart: off, ByteEnd: end,
					Class: damage.ClassParseError, Detail: perr.Error()})
				break
			}
			rep.ParsedOK++
			rep.RTCPPackets += ri.Packets
		default:
			rep.ParseErrors++
			damages = append(damages, damage.Range{ByteStart: off, ByteEnd: end,
				Class: damage.ClassParseError, Detail: fmt.Sprintf("unknown record type %d", typ)})
		}
		off = end
	}
	for _, ev := range reo.Flush() {
		handle(ev)
	}
	rep.Reordered = reo.Reordered
	rep.Duplicates = reo.Dups

	// Resolve sequence-gap losses to byte/tick ranges between neighbours.
	for _, l := range losses {
		r := damage.Range{Class: damage.ClassSeqGap,
			Detail: fmt.Sprintf("lost extended seq %d..%d", l[0], l[1])}
		pi := sort.Search(len(spans), func(i int) bool { return spans[i].extSeq >= l[0] })
		if pi > 0 {
			r.ByteStart = spans[pi-1].recEnd
			r.TickStart = spans[pi-1].tick
		}
		if pi < len(spans) {
			r.ByteEnd = spans[pi].recStart
			r.TickEnd = spans[pi].tick
		}
		damages = append(damages, r)
	}

	// Bitstream scan over the reassembled payload, mapped to input bytes.
	scan := bitstream.Scan(buf, 0)
	spanAt := func(boff int64) *span {
		i := sort.Search(len(spans), func(i int) bool { return spans[i].bufEnd > boff })
		if i == len(spans) {
			return nil
		}
		return &spans[i]
	}
	toInput := func(boff int64) int64 {
		if s := spanAt(boff); s != nil {
			return s.payloadOff + (boff - s.bufStart)
		}
		return -1
	}
	for _, issue := range scan.Issues {
		class := damage.ClassInvalidLength
		if strings.Contains(issue.Reason, "truncated") {
			class = damage.ClassNALTruncated
		}
		start := toInput(issue.Offset)
		if start < 0 {
			continue
		}
		r := damage.Range{ByteStart: start, ByteEnd: start + 4, Class: class, Detail: issue.Reason}
		if s := spanAt(issue.Offset); s != nil {
			r.TickStart, r.TickEnd = s.tick, s.tick
		}
		if class == damage.ClassNALTruncated && len(buf) > 0 {
			r.ByteEnd = toInput(int64(len(buf))-1) + 1
		}
		damages = append(damages, r)
	}
	if len(spans) > 0 && scan.RecoverableBoundary < int64(len(buf)) {
		rep.RecoverableBoundary = toInput(scan.RecoverableBoundary)
		rep.HasBoundary = rep.RecoverableBoundary >= 0
	}

	// Keyframe index: the noindex scenario simulates an incomplete index.
	idx := bitstream.Build(scan)
	if opts.Scenario == "noindex" {
		kept := idx.Offsets[:0]
		for i, off := range idx.Offsets {
			if i%2 == 0 {
				kept = append(kept, off)
			}
		}
		idx.Offsets = kept
	}
	hits, _ := idx.Verify(scan)
	if len(scan.Keyframes) > 0 {
		rep.KeyframeHitRate = float64(hits) / float64(len(scan.Keyframes))
	}
	inIndex := make(map[int64]bool, len(idx.Offsets))
	for _, off := range idx.Offsets {
		inIndex[off] = true
	}
	for _, k := range scan.Keyframes {
		nal := scan.NALs[k]
		if s := spanAt(nal.Offset); s != nil {
			rep.KeyframeTicks = append(rep.KeyframeTicks, s.tick)
		}
		if !inIndex[nal.Offset] {
			if start := toInput(nal.Offset); start >= 0 {
				damages = append(damages, damage.Range{ByteStart: start, ByteEnd: start + 4,
					Class: damage.ClassIndexIncomplete, Detail: "keyframe missing from index"})
			}
		}
	}

	// Final timeline segments: evicted (final) plus retained windows.
	segs := append(append([]clock.Window{}, replay.Evicted...), replay.Flush()...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].ID < segs[j].ID })
	for _, w := range segs {
		rep.Segments = append(rep.Segments, w.Hash)
	}

	if rtpRecords > 0 {
		rep.RoundtripRate = float64(roundtripOK) / float64(rtpRecords)
	}
	rep.Damage = damage.Merge(damages)

	if !monotonic {
		return nil, errors.New("invariant: non-monotonic event time")
	}
	if damage.Overlaps(rep.Damage) {
		return nil, errors.New("invariant: overlapping damage ranges")
	}
	if rep.Packets == 0 {
		return nil, errors.New("unrecoverable: no valid RTP packets")
	}
	if len(scan.Keyframes) == 0 {
		return nil, errors.New("unrecoverable: no keyframes found")
	}
	return rep, nil
}
