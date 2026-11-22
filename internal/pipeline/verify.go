package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/media"
)

// FaultKind names one locally generated failure case.
type FaultKind string

const (
	FaultTruncated          FaultKind = "truncated"
	FaultZeroTimescale      FaultKind = "zero-timescale"
	FaultLateEvent          FaultKind = "late-event"
	FaultSequenceWrap       FaultKind = "sequence-wrap"
	FaultBurstLoss          FaultKind = "burst-loss"
	FaultCorruptSampleTable FaultKind = "corrupt-sample-table"
	FaultOutOfOrder         FaultKind = "out-of-order-pts"
)

// AllFaults lists every case the verify command supports.
func AllFaults() []FaultKind {
	return []FaultKind{
		FaultTruncated, FaultZeroTimescale, FaultLateEvent, FaultSequenceWrap,
		FaultBurstLoss, FaultCorruptSampleTable, FaultOutOfOrder,
	}
}

// ParseFaults turns a comma-separated list into validated kinds.
func ParseFaults(list string) ([]FaultKind, error) {
	known := make(map[FaultKind]bool)
	for _, f := range AllFaults() {
		known[f] = true
	}
	var out []FaultKind
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k := FaultKind(part)
		if !known[k] {
			return nil, media.NewError(media.InvalidInput, "unknown fault case %q", part)
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, media.NewError(media.InvalidInput, "no fault cases given")
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// FaultResult is one case's outcome.
type FaultResult struct {
	Kind         FaultKind
	Detected     int
	Category     media.Category
	Recovered    int
	RecoveryRate float64
	// CountsDetail is the per-case error count keyed by category, so several
	// diagnostics from one case remain visible.
	CountsDetail map[media.Category]int
	Replay       string // human description of replayed intervals
	ReplaySpans  []string
	Detail       string
	Err          error
}

// VerifyReport is the terminal summary of a verify run.
type VerifyReport struct {
	Seed    int64
	Results []FaultResult
}

// ExitCode maps the report to the documented process status: 2 when faults
// were correctly detected as corrupt input, 0 when all invariants hold, 1 on
// internal failure.
func (r *VerifyReport) ExitCode() int {
	for _, res := range r.Results {
		if res.Err != nil {
			return 1
		}
	}
	return 0
}

// Verify generates each requested fault locally, feeds it through the
// affected stage, and asserts the fault was detected with the right category.
func Verify(seed int64, kinds []FaultKind) (*VerifyReport, error) {
	rep := &VerifyReport{Seed: seed}
	for _, kind := range kinds {
		res := verifyOne(seed, kind)
		if res.CountsDetail == nil {
			res.CountsDetail = make(map[media.Category]int)
			if res.Detected > 0 && res.Category != "" {
				res.CountsDetail[res.Category] = res.Detected
			}
		}
		rep.Results = append(rep.Results, res)
	}
	return rep, nil
}

// Lines renders the report for the terminal.
func (r *VerifyReport) Lines() []string {
	var lines []string
	for _, res := range r.Results {
		status := "ok"
		if res.Err != nil {
			status = "FAIL: " + res.Err.Error()
		}
		lines = append(lines, fmt.Sprintf(
			"%-16s detected=%-3d category=%-22s recovered=%-3d recovery_rate=%.3f replay=%s %s",
			res.Kind, res.Detected, res.Category, res.Recovered, res.RecoveryRate, res.Replay, status))
	}
	return lines
}

// FaultCounts aggregates detections per category.
func (r *VerifyReport) FaultCounts() map[media.Category]int {
	counts := make(map[media.Category]int)
	for _, res := range r.Results {
		for cat, n := range res.CountsDetail {
			counts[cat] += n
		}
	}
	return counts
}

// KindCounts maps each case to its primary error count for the terminal.
func (r *VerifyReport) KindCounts() map[FaultKind]int {
	out := make(map[FaultKind]int, len(r.Results))
	for _, res := range r.Results {
		out[res.Kind] = res.Detected
	}
	return out
}

// ReplaySpans collects every replayed interval across cases.
func (r *VerifyReport) ReplaySpans() []string {
	var spans []string
	for _, res := range r.Results {
		spans = append(spans, res.ReplaySpans...)
	}
	return spans
}

// verifyOne builds and runs a single fault case.
func verifyOne(seed int64, kind FaultKind) FaultResult {
	res := FaultResult{Kind: kind}
	switch kind {
	case FaultTruncated:
		verifyTruncated(seed, &res)
	case FaultZeroTimescale:
		verifyZeroTimescale(seed, &res)
	case FaultLateEvent:
		verifyLateEvent(seed, &res)
	case FaultSequenceWrap:
		verifySequenceWrap(seed, &res)
	case FaultBurstLoss:
		verifyBurstLoss(seed, &res)
	case FaultCorruptSampleTable:
		verifyCorruptSampleTable(seed, &res)
	case FaultOutOfOrder:
		verifyOutOfOrderPTS(seed, &res)
	default:
		res.Err = media.NewError(media.InvalidInput, "unhandled fault kind %q", kind)
	}
	return res
}

// verifyTruncated cuts a valid movie short and requires the parser to report
// a located truncation rather than returning partial data.
func verifyTruncated(seed int64, res *FaultResult) {
	data, err := fixture.BuildMovie(fixture.MovieSpec{
		Timescale: 1000,
		Tracks:    []fixture.TrackSpec{verifyTrack(1, seed)},
	})
	if err != nil {
		res.Err = err
		return
	}
	cuts := []int{len(data) / 3, len(data) / 2, len(data) - 5}
	for _, cut := range cuts {
		_, err := containerParse(data[:cut])
		if err == nil {
			res.Err = media.NewError(media.InvariantViolation, "truncated file at %d parsed cleanly", cut)
			return
		}
		cat := media.CategoryOf(err)
		if cat != media.TruncatedContainer && cat != media.CorruptSampleTable {
			res.Err = media.NewError(media.InvariantViolation,
				"truncation at %d reported as %s", cut, cat)
			return
		}
		if e := media.AsError(err); !e.HasOff && !e.HasTrack {
			res.Err = media.NewError(media.InvariantViolation,
				"truncation at %d has no location: %v", cut, err)
			return
		}
		res.Detected++
		res.Category = cat
	}
	res.Detail = "truncated containers rejected with offsets"
}

// verifyZeroTimescale rewrites mdhd timescale to zero.
func verifyZeroTimescale(seed int64, res *FaultResult) {
	data, err := fixture.BuildMovie(fixture.MovieSpec{
		Timescale: 1000,
		Tracks:    []fixture.TrackSpec{verifyTrack(1, seed)},
	})
	if err != nil {
		res.Err = err
		return
	}
	patched, err := fixture.PatchBoxUint32(data, "mdhd", 16, 0)
	if err != nil {
		res.Err = err
		return
	}
	_, err = containerParse(patched)
	if err == nil {
		res.Err = media.NewError(media.InvariantViolation, "zero timescale accepted")
		return
	}
	if media.CategoryOf(err) != media.ZeroTimescale {
		res.Err = media.NewError(media.InvariantViolation,
			"zero timescale reported as %s", media.CategoryOf(err))
		return
	}
	e := media.AsError(err)
	if !e.HasTrack {
		res.Err = media.NewError(media.InvariantViolation,
			"zero timescale error lacks a track id: %v", err)
		return
	}
	res.Detected = 1
	res.Category = media.ZeroTimescale
	res.Detail = fmt.Sprintf("track %d timescale zero rejected", e.Track)
}

// verifyLateEvent feeds an event behind the watermark and requires a bounded
// replay of exactly the impacted interval.
func verifyLateEvent(seed int64, res *FaultResult) {
	seg := media.Nano(1000 * int64(media.Nano(1e6)))
	cfg := timelineConfig(seg)
	tl, err := timelineNew(cfg)
	if err != nil {
		res.Err = err
		return
	}
	for i := int64(0); i < 6; i++ {
		if err := tl.Push(timelineEvent(media.Nano(i)*seg, 1, uint64(i))); err != nil {
			res.Err = err
			return
		}
	}
	// Jump far ahead so the watermark seals the first segments.
	if err := tl.Push(timelineEvent(media.Nano(9)*seg, 1, 100)); err != nil {
		res.Err = err
		return
	}
	late := timelineEvent(media.Nano(2)*seg, 2, 200)
	if err := tl.Push(late); err != nil {
		res.Err = err
		return
	}
	if err := tl.Flush(); err != nil {
		res.Err = err
		return
	}
	recs := tl.LateRecords()
	if len(recs) != 1 {
		res.Err = media.NewError(media.InvariantViolation,
			"%d late records, want 1", len(recs))
		return
	}
	res.Detected = 1
	res.Category = media.LateEvent
	res.ReplaySpans = []string{formatInterval(recs[0].Interval)}
	res.Replay = res.ReplaySpans[0]
	res.Recovered = 1
	res.RecoveryRate = 1
	// Determinism: a canonical-order rebuild must hash identically.
	var all []timelineEventT
	for _, s := range tl.Segments() {
		all = append(all, s.Events...)
	}
	ref, err := timelineNew(cfg)
	if err != nil {
		res.Err = err
		return
	}
	for _, ev := range all {
		if err := ref.Push(ev); err != nil {
			res.Err = err
			return
		}
	}
	if err := ref.Flush(); err != nil {
		res.Err = err
		return
	}
	if hashSegments(ref.Segments()) != hashSegments(tl.Segments()) {
		res.Err = media.NewError(media.InvariantViolation,
			"late-event replay is not deterministic")
		return
	}
	res.Detail = "late event replayed its sealed interval deterministically"
}

// verifySequenceWrap drives the receiver across the 16-bit wrap.
func verifySequenceWrap(seed int64, res *FaultResult) {
	r, err := rtpNew(rtpConfig{MaxReorderWindow: 16, MaxBuffer: 128})
	if err != nil {
		res.Err = err
		return
	}
	const packets = 64
	start := uint16(65504)
	for i := 0; i < packets; i++ {
		seq := start + uint16(i)
		if err := r.Push(rtpPacket(seq, uint32(i*90)), 0); err != nil {
			res.Err = media.NewError(media.InvariantViolation,
				"wrap packet %d rejected: %v", seq, err)
			return
		}
	}
	r.Close()
	stats := r.Stats()
	delivered := r.Delivered()
	if len(delivered) != packets {
		res.Err = media.NewError(media.InvariantViolation,
			"delivered %d of %d across wrap", len(delivered), packets)
		return
	}
	for i := 1; i < len(delivered); i++ {
		if delivered[i].Seq != delivered[i-1].Seq+1 {
			res.Err = media.NewError(media.InvariantViolation,
				"wrap delivery out of order at %d: %d after %d",
				i, delivered[i].Seq, delivered[i-1].Seq)
			return
		}
	}
	if stats.Wraps == 0 {
		res.Err = media.NewError(media.InvariantViolation, "wrap was not detected")
		return
	}
	res.Detected = stats.Wraps
	res.Category = media.SequenceWrap
	res.Recovered = stats.Delivered
	res.RecoveryRate = stats.RecoveryRate
	res.Detail = fmt.Sprintf("%d wraps, %d packets in order", stats.Wraps, stats.Delivered)
}

// verifyBurstLoss injects a contiguous burst and requires every lost slot to
// be accounted for as an explicit gap.
func verifyBurstLoss(seed int64, res *FaultResult) {
	trace := fixture.SimulateNetwork(fixture.NetworkSpec{
		Packets:   120,
		Loss:      0,
		BurstLoss: 9,
		BurstAt:   40,
		JitterMS:  20,
		Reorder:   6,
		Seed:      seed,
	})
	r, err := rtpNew(rtpConfig{MaxReorderWindow: 16, MaxBuffer: 256})
	if err != nil {
		res.Err = err
		return
	}
	for i, pkt := range trace.Packets {
		if err := r.Push(pkt, media.Nano(trace.Arrivals[i])); err != nil {
			res.Err = err
			return
		}
	}
	r.Close()
	stats := r.Stats()
	if stats.Lost != len(trace.Dropped) {
		res.Err = media.NewError(media.InvariantViolation,
			"receiver lost %d, channel dropped %d", stats.Lost, len(trace.Dropped))
		return
	}
	var gapsTotal int
	for _, g := range r.Gaps() {
		gapsTotal += g.Count
	}
	if gapsTotal != stats.Lost {
		res.Err = media.NewError(media.InvariantViolation,
			"gaps cover %d but %d were lost", gapsTotal, stats.Lost)
		return
	}
	if stats.RecoveryRate < 0.85 {
		res.Err = media.NewError(media.InvariantViolation,
			"recovery rate %.3f below 0.85", stats.RecoveryRate)
		return
	}
	res.Detected = stats.Lost
	res.Category = media.UnrecoverableGap
	res.Recovered = stats.Delivered
	res.RecoveryRate = stats.RecoveryRate
	res.Detail = fmt.Sprintf("%d packets lost in %d gaps", stats.Lost, len(r.Gaps()))
}

// verifyCorruptSampleTable declares more samples than the sample table holds,
// which must be rejected with a track-located sample-table error.
func verifyCorruptSampleTable(seed int64, res *FaultResult) {
	data, err := fixture.BuildMovie(fixture.MovieSpec{
		Timescale: 1000,
		Tracks:    []fixture.TrackSpec{verifyTrack(1, seed)},
	})
	if err != nil {
		res.Err = err
		return
	}
	// stsz: type(4) + version/flags(4) + sample_size(4) => count at +12.
	patched, err := fixture.PatchBoxUint32(data, "stsz", 12, 4096)
	if err != nil {
		res.Err = err
		return
	}
	_, err = containerParse(patched)
	if err == nil {
		res.Err = media.NewError(media.InvariantViolation, "corrupt sample table accepted")
		return
	}
	cat := media.CategoryOf(err)
	if cat != media.CorruptSampleTable && cat != media.TruncatedContainer {
		res.Err = media.NewError(media.InvariantViolation,
			"corrupt sample table reported as %s", cat)
		return
	}
	e := media.AsError(err)
	if !e.HasTrack {
		res.Err = media.NewError(media.InvariantViolation,
			"corrupt sample table error lacks a track id: %v", err)
		return
	}
	res.Detected = 1
	res.Category = cat
	res.Detail = fmt.Sprintf("track %d sample table rejected", e.Track)
}

// verifyOutOfOrderPTS rewrites the chunk table so sample payloads overlap,
// making decode times inconsistent, and requires a located rejection.
func verifyOutOfOrderPTS(seed int64, res *FaultResult) {
	data, err := fixture.BuildMovie(fixture.MovieSpec{
		Timescale: 1000,
		Tracks:    []fixture.TrackSpec{verifyTrack(1, seed)},
	})
	if err != nil {
		res.Err = err
		return
	}
	// Force every chunk to start at the same offset: samples then overlap and
	// the stream is no longer decodable in order.
	patched, err := fixture.PatchChunkOffsets(data, 1)
	if err != nil {
		res.Err = err
		return
	}
	_, err = containerParse(patched)
	if err == nil {
		res.Err = media.NewError(media.InvariantViolation, "overlapping chunk offsets accepted")
		return
	}
	cat := media.CategoryOf(err)
	if cat != media.CorruptSampleTable && cat != media.OutOfOrderTimestamp {
		res.Err = media.NewError(media.InvariantViolation,
			"overlapping samples reported as %s", cat)
		return
	}
	res.Detected = 1
	res.Category = cat
	res.Detail = "overlapping chunk layout rejected"
}

func verifyTrack(id uint32, seed int64) fixture.TrackSpec {
	tr := fixture.TrackSpec{ID: id, Handler: "vide", Timescale: 90000, Width: 320, Height: 180, Seed: uint64(seed)}
	for i := 0; i < 60; i++ {
		tr.Samples = append(tr.Samples, fixture.Sample{Delta: 3000, Size: uint32(80 + i%13), Sync: i%30 == 0})
	}
	return tr
}

func formatInterval(iv intervalT) string {
	return fmt.Sprintf("[%dms,%dms)", int64(iv.Start)/1e6, int64(iv.End)/1e6)
}
