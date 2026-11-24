package selftest

import (
	"context"
	"path/filepath"
	"sort"

	pionrtp "github.com/pion/rtp"

	"mediacheck/internal/adapter"
	"mediacheck/internal/clock"
	"mediacheck/internal/fault"
	"mediacheck/internal/remux"
	rtpx "mediacheck/internal/rtp"
)

// RTPOutcome carries the RTP-specific results of one sample.
type RTPOutcome struct {
	Stats  rtpx.Stats
	Ranges []rtpx.Range
}

// ProcessRTP runs the RTP path: capture read, jitter buffering, loss
// recovery classification, frame reassembly, remux and verification.
// Lost media is never fabricated; incomplete frames are dropped and the
// affected ranges are reported.
func ProcessRTP(ctx context.Context, cfg Config, name, path, outDir string, out *RTPOutcome) *Result {
	fl := fault.NewList()
	res := &Result{Name: name, Faults: fl}

	pkts, offsets, err := ReadRTPCap(path)
	if err != nil {
		fl.Add(fault.ErrTruncated, -1, "%v", err)
		res.Skipped = true
		return res
	}
	if len(pkts) == 0 {
		fl.Add(fault.ErrTruncated, 0, "empty capture")
		res.Skipped = true
		return res
	}
	seqToOff := make(map[uint16]int64, len(pkts))
	for i, p := range pkts {
		if _, ok := seqToOff[p.SequenceNumber]; !ok {
			seqToOff[p.SequenceNumber] = offsets[i]
		}
	}

	// Arrival-order wraparound check on the 32-bit RTP clock.
	wt := clock.NewWrapTracker(fl)
	for i, p := range pkts {
		wt.Check(p.Timestamp, offsets[i])
	}

	window := windowPackets(cfg.JitterMs, pkts)
	buf := rtpx.NewBuffer(window)
	var ordered []*pionrtp.Packet
	for _, p := range pkts {
		ordered = append(ordered, buf.Push(p)...)
	}
	ordered = append(ordered, buf.Flush()...)

	// Emitted-order monotonicity check on the shared clock.
	var lastTS uint32
	started := false
	for _, p := range ordered {
		if started && p.Timestamp < lastTS {
			fl.Add(fault.ErrNonMonotonic, seqToOff[p.SequenceNumber],
				"RTP timestamp %d goes backwards (previous %d)", p.Timestamp, lastTS)
		}
		lastTS = p.Timestamp
		started = true
	}

	st := buf.Stats()
	out.Stats = st
	lost := buf.Lost()
	ranges := rtpx.Classify(ordered, lost, RTPClock)
	out.Ranges = ranges
	for _, r := range ranges {
		fl.Add(fault.ErrPacketLoss, -1,
			"seq %d-%d lost (%d packets), media range [%dms,%dms]",
			r.FirstLostSeq, r.LastLostSeq, r.LostCount, r.StartNanos/1e6, r.EndNanos/1e6)
		if !r.Recoverable {
			fl.Add(fault.ErrUnrecoverable, -1,
				"range [%dms,%dms] cannot be recovered (no keyframe restart)",
				r.StartNanos/1e6, r.EndNanos/1e6)
		}
	}

	samples, payloads, frameDur := reassemble(ordered, lost, fl)
	if len(samples) == 0 {
		fl.Add(fault.ErrUnrecoverable, -1, "no complete frames could be reassembled")
		res.Skipped = true
		return res
	}
	// Media duration covers every generated frame, including lost ones.
	maxFrame := uint32(0)
	for _, p := range ordered {
		if len(p.Payload) >= 5 {
			if fi := frameOf(p); fi > maxFrame {
				maxFrame = fi
			}
		}
	}
	res.DurationNs = RTPClock.ToNanos(int64(maxFrame+1) * frameDur)

	overlays := make([]rangeOverlay, len(ranges))
	for i, r := range ranges {
		overlays[i] = rangeOverlay{StartNanos: r.StartNanos, EndNanos: r.EndNanos, Recoverable: r.Recoverable}
	}
	res.Manifest = buildManifest(name, res.DurationNs, cfg.SegmentNs, nil, overlays)

	track := &adapter.Track{ID: 1, Kind: adapter.KindVideo, Timescale: RTPClock, Samples: samples}
	specs := []remux.TrackSpec{{ID: 1, Kind: adapter.KindVideo, Timescale: RTPClock, Samples: track.Samples}}
	src := func(ti, si int) ([]byte, error) { return payloads[si], nil }
	outPath := filepath.Join(outDir, name+".out.mp4")
	written, peak, err := streamRemux(ctx, cfg, specs, res.DurationNs, src, outPath)
	res.PeakBuffer = peak
	if err != nil {
		fl.Add(fault.ErrCanceled, -1, "remux: %v", err)
		return res
	}
	res.Remuxed = true
	res.OutPath = outPath

	finalizeChecks(res, cfg, fl, written, RTPClock.ToNanos(frameDur))
	return res
}

// windowPackets converts the jitter window from milliseconds to packets
// using the observed packet rate of the capture.
func windowPackets(jitterMs int, pkts []*pionrtp.Packet) int {
	if len(pkts) < 2 {
		return 1
	}
	span := int64(pkts[len(pkts)-1].Timestamp) - int64(pkts[0].Timestamp)
	if span <= 0 {
		span = 1
	}
	// packets per tick * ticks-per-ms * jitterMs
	pps := float64(len(pkts)) / (float64(span) / float64(RTPClock))
	w := int(pps * float64(jitterMs) / 1000)
	if w < 1 {
		w = 1
	}
	return w
}

// reassemble rebuilds complete frames from ordered packets. A frame is
// complete when its packets are sequence-contiguous and end with the
// marker bit. Incomplete frames are dropped, never fabricated.
func reassemble(ordered []*pionrtp.Packet, lost []uint16, fl *fault.List) ([]adapter.Sample, [][]byte, int64) {
	lostSet := make(map[uint16]bool, len(lost))
	for _, s := range lost {
		lostSet[s] = true
	}
	type acc struct {
		chunks   [][]byte
		minSeq   uint16
		maxSeq   uint16
		count    int
		keyframe bool
		marker   bool
	}
	frames := map[uint32]*acc{}
	for _, p := range ordered {
		if len(p.Payload) < 5 {
			fl.Add(fault.ErrUnsupportedTrack, -1, "packet seq %d: payload too short", p.SequenceNumber)
			continue
		}
		fi := frameOf(p)
		a := frames[fi]
		if a == nil {
			a = &acc{minSeq: p.SequenceNumber, maxSeq: p.SequenceNumber}
			frames[fi] = a
		}
		a.chunks = append(a.chunks, p.Payload[5:])
		a.count++
		if rtpx.SeqLess(p.SequenceNumber, a.minSeq) {
			a.minSeq = p.SequenceNumber
		}
		if rtpx.SeqLess(a.maxSeq, p.SequenceNumber) {
			a.maxSeq = p.SequenceNumber
		}
		if p.Payload[4]&0x01 != 0 {
			a.keyframe = true
		}
		if p.Marker {
			a.marker = true
		}
	}
	fis := make([]int, 0, len(frames))
	for fi := range frames {
		fis = append(fis, int(fi))
	}
	sort.Ints(fis)

	// Frame duration from the RTP timestamps of the first two frames.
	frameDur := FrameDuration
	var samples []adapter.Sample
	var payloads [][]byte
	for _, fi := range fis {
		a := frames[uint32(fi)]
		span := int(a.maxSeq-a.minSeq) + 1
		complete := a.marker && a.count == span
		if !complete {
			continue // dropped, reported via loss ranges
		}
		var size int
		for _, c := range a.chunks {
			size += len(c)
		}
		payload := make([]byte, 0, size)
		for _, c := range a.chunks {
			payload = append(payload, c...)
		}
		samples = append(samples, adapter.Sample{
			DTS: int64(fi) * frameDur, CTS: int64(fi) * frameDur,
			Duration: frameDur, Size: uint32(size), Keyframe: a.keyframe,
		})
		payloads = append(payloads, payload)
	}
	return samples, payloads, frameDur
}
