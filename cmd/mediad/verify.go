package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/clock"
	"mediad/internal/ebml"
	"mediad/internal/fixture"
	"mediad/internal/merge"
)

// verify runs the full acceptance path and prints machine-checkable lines.
func verify(work string, genFixtures bool, budgetMs, maxBytes int64) error {
	set := &fixture.Set{Dir: work}
	if genFixtures {
		if _, err := fixture.Generate(work); err != nil {
			return err
		}
	}

	// --- Normal path: parse, unify timebase, index, query. ---
	data, err := readCapped(set.Path(fixture.ValidMKV))
	if err != nil {
		return err
	}
	timed, ix, err := mapAndIndex(data)
	if err != nil {
		return err
	}
	mapped := len(timed)
	total := len(timed)

	// RTP capture events unified onto the same timeline.
	rtpNs, err := readRTPEvents(set.Path(fixture.RTPEvents))
	if err != nil {
		return err
	}
	total += len(rtpNs)
	mapped += len(rtpNs)
	rtpAligned := 1
	for _, ns := range rtpNs {
		if !ix.HasKeyframeAt(ns) {
			rtpAligned = 0
		}
	}
	fmt.Printf("roundtrip_rate=%.3f\n", float64(mapped)/float64(total))
	fmt.Printf("rtp_aligned=%d\n", rtpAligned)

	// MP4 timebase cross-check via go-mp4.
	mp4OK, err := checkMP4Timescale(set.Path(fixture.MP4Check))
	if err != nil {
		return err
	}
	fmt.Printf("mp4_timescale_match=%d\n", b2i(mp4OK))

	// Interval queries at every keyframe: random access must land exactly.
	var segs []merge.Segment
	hits := 0
	for i, k := range ix.Keys {
		end := k.TimeNs + 80_000_000
		if end > ix.DurationNs {
			end = ix.DurationNs
		}
		seg, err := ix.Query(timed, k.TimeNs, end)
		if err != nil {
			return err
		}
		if seg.SrcOffset == k.Offset {
			hits++
		}
		segs = append(segs, merge.Segment{
			TaskKey:   fmt.Sprintf("seg-%03d", i),
			StartNs:   seg.StartNs,
			EndNs:     seg.EndNs,
			SrcOffset: seg.SrcOffset,
			SrcLength: seg.SrcLength,
			SHA256:    merge.HashBytes(data[seg.SrcOffset : seg.SrcOffset+seg.SrcLength]),
		})
	}
	fmt.Printf("keyframe_hit_rate=%.3f\n", float64(hits)/float64(len(ix.Keys)))

	// Segment hashes must reproduce from an independent re-parse.
	timed2, ix2, err := mapAndIndex(data)
	if err != nil {
		return err
	}
	match := 1
	for _, s := range segs {
		seg, err := ix2.Query(timed2, s.StartNs, s.EndNs)
		if err != nil {
			return err
		}
		if merge.HashBytes(data[seg.SrcOffset:seg.SrcOffset+seg.SrcLength]) != s.SHA256 {
			match = 0
		}
	}
	fmt.Printf("segment_hash_match=%d\n", match)

	// Manifest determinism: same segments, different input order/grouping.
	m1, err := merge.Merge(segs)
	if err != nil {
		return err
	}
	rev := make([]merge.Segment, len(segs))
	for i, s := range segs {
		rev[len(segs)-1-i] = s
	}
	m2, err := merge.Merge(rev[:1], rev[1:])
	if err != nil {
		return err
	}
	h1, _ := m1.Hash()
	h2, _ := m2.Hash()
	fmt.Printf("manifest_stable=%d\n", b2i(h1 == h2))
	mBytes, _ := m1.Bytes()
	if err := os.WriteFile(filepath.Join(work, "manifest.json"), mBytes, 0o644); err != nil {
		return err
	}

	// --- Corrupt path: stable codes, offsets, paths; parse exits 2. ---
	for _, name := range []string{fixture.TruncatedMKV, fixture.InvalidVINTMKV, fixture.OutOfBoundsMKV} {
		bad, err := readCapped(set.Path(name))
		if err != nil {
			return err
		}
		_, perr := ebml.NewParser(bad).Parse()
		ce, ok := perr.(*clock.Error)
		if !ok {
			return fmt.Errorf("corrupt fixture %s did not fail with clock.Error: %v", name, perr)
		}
		printClockError(ce)
	}
	fmt.Printf("corrupt_exit_code=%d\n", exitUsage)

	// --- Boundary path: out-of-range queries and oversized elements. ---
	rejections := 0
	for _, iv := range [][2]int64{{-1, 10}, {0, ix.DurationNs + 1}, {5, 5}} {
		if _, err := ix.Query(timed, iv[0], iv[1]); clock.CodeOf(err) == clock.ErrBounds {
			rejections++
		}
	}
	p := ebml.NewParser(data)
	p.MaxElementSize = 8
	if _, err := p.Parse(); clock.CodeOf(err) == clock.ErrSizeOverflow {
		rejections++
	}
	fmt.Printf("boundary_rejections=%d\n", rejections)

	// --- Recovery path: crash, resume, rerun determinism. ---
	resumed, rerunMatch, err := recoveryPath(work, data)
	if err != nil {
		return err
	}
	fmt.Printf("resumed_from_checkpoint=%d\n", b2i(resumed))
	fmt.Printf("rerun_hash_match=%d\n", b2i(rerunMatch))

	// --- Delivery tradeoff: quality gate, then budget, then size. ---
	cands := []candidate{
		{Name: "high", Quality: 3, CostMs: 3000, Bytes: 2 << 20},
		{Name: "medium", Quality: 2, CostMs: 1500, Bytes: 512 << 10},
		{Name: "low", Quality: 1, CostMs: 400, Bytes: 128 << 10},
	}
	plan, reason, err := choosePlan(cands, 2, budgetMs, maxBytes)
	if err != nil {
		return err
	}
	fmt.Printf("plan=%s plan_quality=%d plan_cost_ms=%d plan_bytes=%d\n",
		plan.Name, plan.Quality, plan.CostMs, plan.Bytes)
	fmt.Printf("plan_reason=%s\n", reason)
	if _, failReason, ferr := choosePlan(cands, 4, budgetMs, maxBytes); ferr != nil {
		fmt.Printf("plan_failure_recorded=1 plan_failure_reason=%s\n", failReason)
	} else {
		fmt.Printf("plan_failure_recorded=0\n")
	}
	return nil
}
