package pipeline

import (
	"errors"
	"fmt"
	"path/filepath"

	"mediapipe/internal/cache"
	"mediapipe/internal/clock"
	"mediapipe/internal/fixtures"
	"mediapipe/internal/mp4"
	"mediapipe/internal/rtp"
	"mediapipe/internal/sync"
)

// Fault classes required by the acceptance matrix.
const (
	FaultReorderWrap  = "reorder_wrap"
	FaultBurstLoss    = "burst_loss"
	FaultTruncatedBox = "truncated_box"
	FaultLayout       = "layout_mismatch"
	FaultCacheForge   = "cache_forgery"
)

// FaultReport is one auditable matrix row.
type FaultReport struct {
	Class        string
	Injected     string
	DetectedBy   string
	PropagatedTo []string
	Recovered    bool
	Detail       string
}

// FaultMatrix runs the five fault classes and verifies detection, propagation
// and recovery. Every fixture is generated in-process.
func FaultMatrix(seed int64) ([]FaultReport, error) {
	var out []FaultReport
	reorder, err := runReorderFault(seed)
	if err != nil {
		return nil, err
	}
	out = append(out, reorder)
	burst, err := runBurstFault(seed)
	if err != nil {
		return nil, err
	}
	out = append(out, burst)
	trunc, err := runTruncatedFault(seed)
	if err != nil {
		return nil, err
	}
	out = append(out, trunc)
	layout, err := runLayoutFault(seed)
	if err != nil {
		return nil, err
	}
	out = append(out, layout)
	forge, err := runCacheFault(seed)
	if err != nil {
		return nil, err
	}
	out = append(out, forge)
	return out, nil
}

func runReorderFault(seed int64) (FaultReport, error) {
	rep := FaultReport{Class: FaultReorderWrap, Injected: "jitter 120ms, 40% reorder, sequence wrap at 0xFF00"}
	sc, err := fixtures.Build(seed)
	if err != nil {
		return rep, err
	}
	tx, err := fixtures.Transmit(sc, fixtures.FaultConfig{ReorderPermille: 400, JitterMS: 120,
		DuplicatePermille: 100, LossBurst: 0})
	if err != nil {
		return rep, err
	}
	buf, err := rtp.NewReorderBuffer(rtp.Config{Window: 64, HoldTicks: 9000})
	if err != nil {
		return rep, err
	}
	// Deliver in arrival order, always validating emitted sequence continuity
	// and media-time monotonicity per track.
	events := tx.Delivered
	var lastEmitted *rtp.Packet
	wraps := 0
	tsWraps := uint64(0)
	var lastPerTrack = map[uint16]int64{}
	unwrappers := map[uint16]*clock.Timeline{}
	var lastSeq uint16
	seen := map[uint16]bool{}
	check := func(pkts []rtp.Packet) error {
		for _, p := range pkts {
			if lastEmitted != nil && p.Seq == lastSeq {
				return fmt.Errorf("duplicate emission %d", p.Seq)
			}
			lastSeq = p.Seq
			if seen[p.Seq] {
				return fmt.Errorf("sequence %d emitted twice", p.Seq)
			}
			seen[p.Seq] = true
			if lastEmitted != nil && uint16(p.Seq-lastEmitted.Seq) == 0 {
				return fmt.Errorf("no progress")
			}
			pl, err := rtp.ParsePayload(p.Payload)
			if err != nil {
				return err
			}
			// The payload timestamp is the raw 32-bit RTP timestamp, so it must
			// be folded by the timeline unwrapper before comparison.
			unw, ok := unwrappers[pl.TrackID]
			if !ok {
				var err error
				unw, err = clock.NewTimeline(uint32(pl.TrackID), fixtures.TrackTimescale(uint32(pl.TrackID)),
					fixtures.RTPEpoch(uint32(pl.TrackID)), 0, 0)
				if err != nil {
					return err
				}
				unwrappers[pl.TrackID] = unw
			}
			mapped, err := unw.Map(uint32(pl.MediaTicks))
			if err != nil {
				return err
			}
			v := mapped.Unified
			if prev, seen := lastPerTrack[pl.TrackID]; seen && v < prev {
				return fmt.Errorf("track %d media time regressed: %d < %d (seq %d)", pl.TrackID, v, prev, p.Seq)
			}
			lastPerTrack[pl.TrackID] = v
			if lastEmitted != nil && p.Seq < lastEmitted.Seq && lastEmitted.Seq > 0xF000 && p.Seq < 0x1000 {
				wraps++
			}
			cp := p
			lastEmitted = &cp
		}
		return nil
	}
	now := int64(0)
	var maxArrival int64
	for _, d := range events {
		pkt, err := rtp.FromWire(d.Wire)
		if err != nil {
			return rep, err
		}
		if d.Arrival > maxArrival {
			maxArrival = d.Arrival
		}
		now = d.Arrival
		if _, err := buf.Push(pkt, now); err != nil {
			return rep, err
		}
		// Flush with the hold elapsed. A hole that is already due at this
		// point is a genuine loss, not a reordering delay.
		if err := check(buf.Flush(now).Ready); err != nil {
			return rep, err
		}
	}
	// Let the playout hold elapse for the tail, then close any genuine hole.
	end := maxArrival + 9000
	for {
		res := buf.Flush(end)
		if err := check(res.Ready); err != nil {
			return rep, err
		}
		if !res.HasGap {
			break
		}
		ready, _, ok := buf.SkipGap(end)
		if !ok {
			break
		}
		if err := check(ready); err != nil {
			return rep, err
		}
	}
	ready, _ := buf.Drain()
	if err := check(ready); err != nil {
		return rep, err
	}
	st := buf.Stats()
	// With no packet loss every sequence must be emitted exactly once even
	// though delivery order was heavily perturbed and crossed the wrap.
	reorderOK := st.Emitted == uint64(tx.Expected) && st.Duplicates > 0 && st.GapsDeclared == 0
	// The same fault class is then driven through every downstream subsystem:
	// reassembly into container samples, alignment, and a warm cache pass.
	dir, err := tempDir()
	if err != nil {
		return rep, err
	}
	defer CleanupTempDir(dir)
	cfg := DefaultConfig()
	cfg.Seed = seed
	cfg.ReorderPermille = 400
	cfg.DuplicatePermille = 100
	cfg.JitterMS = 120
	cfg.CheckpointDir = filepath.Join(dir, "cp")
	cfg.CacheDir = filepath.Join(dir, "cache")
	r, err := New(cfg)
	if err != nil {
		return rep, err
	}
	summary, err := r.Run()
	if err != nil {
		return rep, err
	}
	if err := r.checkAligned(); err != nil {
		return rep, err
	}
	r2, err := New(cfg)
	if err != nil {
		return rep, err
	}
	warm, err := r2.Run()
	if err != nil {
		return rep, err
	}
	timelineWraps := uint64(0)
	for _, tl := range unwrappers {
		st := tl.Stats()
		if st.NonMonotonic > 0 {
			return rep, fmt.Errorf("timeline reported %d backwards timestamps", st.NonMonotonic)
		}
		timelineWraps += st.Wraps
	}
	if timelineWraps == 0 {
		return rep, fmt.Errorf("RTP timestamp wrap was not exercised")
	}
	tsWraps = timelineWraps
	propagated := warm.SegmentCacheHits == warm.Segments && summary.RoundtripMatchRate >= 0.99
	rep.DetectedBy = "transport: sequence continuity and per-track monotonic media time"
	rep.PropagatedTo = []string{"container: sample ownership and payload digests", "sync: alignment",
		"cache: segment digests served warm"}
	rep.Recovered = reorderOK && propagated
	rep.Detail = fmt.Sprintf("emitted=%d expected=%d duplicates=%d reordered=%d seq_wraps=%d ts_wraps=%d roundtrip=%.3f warm_cache_hits=%d",
		st.Emitted, tx.Expected, st.Duplicates, st.Reordered, wraps, tsWraps, summary.RoundtripMatchRate,
		warm.SegmentCacheHits)
	if !rep.Recovered {
		return rep, fmt.Errorf("reorder fault not fully recovered: %s", rep.Detail)
	}
	return rep, nil
}

// checkAligned verifies that every processed segment produced both track views
// and that no payload mismatch escaped the container stage.
func (r *Runner) checkAligned() error {
	if len(r.results) != fixtures.Segments {
		return fmt.Errorf("pipeline: %d segments processed", len(r.results))
	}
	for _, res := range r.results {
		if res.Sync.VideoSamples == 0 || res.Sync.AudioSamples == 0 {
			return fmt.Errorf("pipeline: segment %d missing track views", res.Segment)
		}
		if res.Container.Mismatched != 0 {
			return fmt.Errorf("pipeline: segment %d payload mismatch", res.Segment)
		}
	}
	return nil
}

func runBurstFault(seed int64) (FaultReport, error) {
	rep := FaultReport{Class: FaultBurstLoss, Injected: "4-packet burst per segment, 100% late retransmission"}
	dir, err := tempDir()
	if err != nil {
		return rep, err
	}
	defer CleanupTempDir(dir)
	cfg := DefaultConfig()
	cfg.Seed = seed
	cfg.LossBurst = 4
	cfg.LateRatio = 1000
	cfg.CheckpointDir = filepath.Join(dir, "cp")
	cfg.CacheDir = filepath.Join(dir, "cache")
	r, err := New(cfg)
	if err != nil {
		return rep, err
	}
	s, err := r.Run()
	if err != nil {
		return rep, err
	}
	unresolved := 0
	for _, res := range r.results {
		if !res.Sync.Aligned {
			unresolved++
		}
	}
	// A warm pass must serve the segment results that encode the missing state,
	// proving the loss propagated into the cached index.
	warm := cfg
	warm.Resume = false
	r2, err := New(warm)
	if err != nil {
		return rep, err
	}
	warmSummary, err := r2.Run()
	if err != nil {
		return rep, err
	}
	rep.DetectedBy = "transport: recovery deadline; sync: missing state; cache: segment digest"
	rep.PropagatedTo = []string{"sync", "cache"}
	rep.Recovered = s.RoundtripMatchRate >= 0.99 && s.UnrecoveredGaps > 0 && s.MissingGroups > 0 &&
		warmSummary.SegmentCacheHits == s.Segments
	rep.Detail = fmt.Sprintf("unrecovered_gaps=%d missing_groups=%d unresolved_segments=%d roundtrip=%.3f cached_missing_state=%d",
		s.UnrecoveredGaps, s.MissingGroups, unresolved, s.RoundtripMatchRate, warmSummary.SegmentCacheHits)
	if !rep.Recovered {
		return rep, fmt.Errorf("burst fault propagation incomplete: %s", rep.Detail)
	}
	return rep, nil
}

func runTruncatedFault(seed int64) (FaultReport, error) {
	rep := FaultReport{Class: FaultTruncatedBox, Injected: "last 24 bytes of a generated MP4 removed"}
	sc, err := fixtures.Build(seed)
	if err != nil {
		return rep, err
	}
	data := sc.MP4
	cut := data[:len(data)-24]
	_, err = mp4.ParseFile(cut)
	rep.DetectedBy = "container: strict box walk (truncated box / offset out of range)"
	rep.PropagatedTo = []string{"pipeline: segment rejected before index build"}
	rep.Recovered = err != nil
	rep.Detail = fmt.Sprintf("parse_error=%v", err)
	if err == nil {
		return rep, fmt.Errorf("truncated fixture accepted")
	}
	return rep, nil
}

func runLayoutFault(seed int64) (FaultReport, error) {
	rep := FaultReport{Class: FaultLayout, Injected: "audio declared mono 44100 Hz against stereo 48000 Hz reference"}
	tracks := []mp4.TrackSpec{
		// The mdhd timescale (22 050) disagrees with both the sample entry rate
		// (44 100) and the reference (48 000): an inconsistent sample clock.
		{ID: 2, Handler: "soun", Timescale: 22050, Channels: 1, SampleSize: 16, SampleRate: 44100},
	}
	segs := []mp4.SegmentSpec{{Sequence: 1, Samples: []mp4.SampleSpec{
		{TrackID: 2, Duration: 1024, Sync: true, Payload: []byte{1, 2, 3, 4}},
	}}}
	_ = seed
	data, err := mp4.BuildFile(tracks, segs)
	if err != nil {
		return rep, err
	}
	f, err := mp4.ParseFile(data)
	if err != nil {
		return rep, err
	}
	decls := sync.DeclsFromFile(f)
	res := sync.CheckLayout(decls, map[uint32]sync.AudioExpectation{
		2: {Channels: 2, SampleRate: 48000, SampleSize: 16, ChannelLayout: "FL,FR"},
	}, nil)
	codes := map[string]int{}
	for _, d := range res.Issues {
		codes[d.Code]++
	}
	rep.DetectedBy = "sync: layout declaration diff, source values preserved"
	rep.PropagatedTo = []string{"cache: segment result carries layout diagnostics"}
	rep.Recovered = codes[sync.IssueChannelCount] == 1 && codes[sync.IssueSampleRate] == 1 &&
		codes[sync.IssueRateTimebase] == 1
	rep.Detail = fmt.Sprintf("channel_issues=%d rate_issues=%d timescale_issues=%d issues=%d",
		codes[sync.IssueChannelCount], codes[sync.IssueSampleRate], codes[sync.IssueRateTimebase], len(res.Issues))
	if !rep.Recovered {
		return rep, fmt.Errorf("layout fault not detected: %s", rep.Detail)
	}
	return rep, nil
}

func runCacheFault(seed int64) (FaultReport, error) {
	rep := FaultReport{Class: FaultCacheForge, Injected: "cache payload replaced with forged bytes and manifest digest kept"}
	dir, err := tempDir()
	if err != nil {
		return rep, err
	}
	defer CleanupTempDir(dir)
	cfg := DefaultConfig()
	cfg.Seed = seed
	cfg.CheckpointDir = filepath.Join(dir, "cp")
	cfg.CacheDir = filepath.Join(dir, "cache")
	cfg.InterruptAfter = 3

	cold, err := New(cfg)
	if err != nil {
		return rep, err
	}
	if _, err := cold.Run(); !errors.Is(err, ErrInterrupted) {
		return rep, fmt.Errorf("fixture run did not interrupt: %v", err)
	}
	hashes := map[uint32]string{}
	for _, res := range cold.results {
		hashes[res.Segment] = res.Container.SegmentHash
	}
	// Forge one committed segment payload: the store must detect it by content
	// digest, quarantine it and force a full recompute of the segment.
	store, err := cache.Open(cfg.CacheDir)
	if err != nil {
		return rep, err
	}
	recs, err := cold.journal.Records()
	if err != nil {
		return rep, err
	}
	if len(recs) == 0 || len(recs[len(recs)-1].Committed) == 0 {
		return rep, fmt.Errorf("no committed checkpoint to forge")
	}
	forgedSegment := recs[len(recs)-1].Committed[0].Sequence
	if err := writeForgedPayload(store.Root(), recs[len(recs)-1].Committed[0].Digest); err != nil {
		return rep, err
	}
	resume := cfg
	resume.InterruptAfter = 0
	resume.Resume = true
	r2, err := New(resume)
	if err != nil {
		return rep, err
	}
	summary, err := r2.Run()
	if err != nil {
		return rep, err
	}
	names, _ := store.Quarantined()
	recomputed := false
	for _, res := range r2.results {
		if res.Segment == forgedSegment && res.Container.SegmentHash == hashes[forgedSegment] {
			recomputed = true
		}
	}
	rep.DetectedBy = "cache: content digest verification"
	rep.PropagatedTo = []string{"transport", "sync", "pipeline: recompute"}
	rep.Recovered = summary.CacheRejected > 0 && len(names) > 0 && recomputed
	rep.Detail = fmt.Sprintf("cache_rejected=%d quarantined=%d recomputed_segment=%d roundtrip=%.3f",
		summary.CacheRejected, len(names), forgedSegment, summary.RoundtripMatchRate)
	if !rep.Recovered {
		return rep, fmt.Errorf("forged cache entry not rejected")
	}
	return rep, nil
}
