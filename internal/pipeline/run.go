// Package pipeline wires ingest, rtp, mp4, timeline, index, repair and report
// into one deterministic acceptance run over generated fixtures.
package pipeline

import (
	"fmt"

	"medcheck/internal/fixture"
	"medcheck/internal/index"
	"medcheck/internal/model"
	"medcheck/internal/mp4"
	"medcheck/internal/repair"
	"medcheck/internal/report"
	rtppkg "medcheck/internal/rtp"
	"medcheck/internal/timeline"
)

// Result holds every measured acceptance metric and the hashes used to prove
// repeatability.
type Result struct {
	ParseRoundTrip     float64
	KeyframeHit        float64
	BadSegments        int
	OffsetTrace        float64
	ReplayEqual        bool
	SegmentSHA256Count int

	SummaryHashForward string
	SummaryHashReverse string
	ShardHashes        []string
	SegmentHashes      []string

	MP4Trials        int
	MP4OK            int
	OffsetTotal      int
	OffsetMapped     int
	KeyframeQueries  int
	KeyframeHits     int
	RecomputeForward int
	RecomputeReverse int
	Notes            []string
}

// Config controls a run.
type Config struct {
	LateWindowMicros  int64
	ShardWindowMicros int64
}

// Run executes the full deterministic pipeline.
func Run(cfg Config) (*Result, error) {
	if cfg.ShardWindowMicros <= 0 {
		cfg.ShardWindowMicros = 100
	}
	res := &Result{}

	// --- MP4 build -> scan round trip, including truncation recovery.
	if err := runMP4(res); err != nil {
		return nil, err
	}

	// --- RTP reordering.
	if err := runRTP(res); err != nil {
		return nil, err
	}

	// --- Wrap-around unwrap.
	if err := runWrap(res); err != nil {
		return nil, err
	}

	// --- Frame-based sets through timeline/index/repair/report.
	var allOrdered []*model.Frame
	for _, set := range fixture.AllFrameSets() {
		view := timeline.Replay(set.Frames, cfg.LateWindowMicros)
		allOrdered = append(allOrdered, view.Ordered...)
		segs := repair.Build(view.Ordered)
		res.BadSegments += len(segs)
		for _, s := range segs {
			if s.SHA256 != "" {
				res.SegmentSHA256Count++
				res.SegmentHashes = append(res.SegmentHashes, s.SHA256)
			}
		}
	}

	// --- Offset traceability across every generated frame.
	trace := repair.NewOffsetMap(allOrdered)
	res.OffsetTotal = len(allOrdered)
	for _, f := range allOrdered {
		if off, ok := trace.Lookup(f.Offset.Stream, f.Track, f.Source, f.Seq); ok && off == f.Offset {
			res.OffsetMapped++
		}
	}
	segsAll := repair.Build(allOrdered)
	if _, _, ok := repair.VerifyOffsets(segsAll, trace); !ok {
		return nil, fmt.Errorf("offset verification failed: a repair moved a byte position")
	}
	if res.OffsetTotal > 0 {
		res.OffsetTrace = float64(res.OffsetMapped) / float64(res.OffsetTotal)
	}

	// --- Key frame index query hit rate over valid frames.
	idx := index.Build(allOrdered)
	for _, e := range idx.Entries() {
		res.KeyframeQueries++
		if got, ok := idx.Exact(e.Track, e.Micros); ok && got.Offset.Start == e.Offset.Start {
			res.KeyframeHits++
		}
	}
	if res.KeyframeQueries > 0 {
		res.KeyframeHit = float64(res.KeyframeHits) / float64(res.KeyframeQueries)
	} else {
		res.KeyframeHit = 1
	}

	// --- Late-frame replay in forward and reverse arrival order.
	if err := runLateReplay(res, cfg); err != nil {
		return nil, err
	}

	// --- Representative shard hashes from the full deterministic order.
	shards := report.BuildShards(allOrdered, cfg.ShardWindowMicros)
	for _, sh := range shards {
		res.ShardHashes = append(res.ShardHashes, sh.SHA256)
	}

	return res, nil
}

func runMP4(res *Result) error {
	good, err := fixture.ValidMP4()
	if err != nil {
		return err
	}
	res.MP4Trials++
	scan, err := mp4.ParseBytes(good.Data, good.Name)
	if err != nil {
		return err
	}
	ok := len(scan.Tracks) == 1 &&
		scan.Tracks[0].TrackID == good.TrackID &&
		scan.Tracks[0].Timescale.Num == uint64(good.Timescale) &&
		len(scan.Tracks[0].Samples) == good.SampleCount &&
		len(scan.Tracks[0].KeyFrames) == len(good.KeySamples) &&
		len(scan.Errors) == 0
	if ok {
		for i, want := range good.KeySamples {
			if scan.Tracks[0].KeyFrames[i] != want {
				ok = false
			}
		}
	}
	if ok {
		res.MP4OK++
	} else {
		res.Notes = append(res.Notes, "valid mp4 round trip mismatch")
	}

	// Truncated sample must locate the bad box at its start offset and still
	// recover the earlier boxes.
	trunc, err := fixture.TruncatedMP4()
	if err != nil {
		return err
	}
	res.MP4Trials++
	tscan, err := mp4.ParseBytes(trunc.Data, trunc.Name)
	if err != nil {
		return err
	}
	foundTrunc := false
	for _, ce := range tscan.Errors {
		if ce.Kind == model.KindTruncatedBox && ce.FirstPos == int64(len(good.Data)) {
			foundTrunc = true
		}
	}
	if foundTrunc && len(tscan.Boxes) >= 3 {
		res.MP4OK++
	} else {
		res.Notes = append(res.Notes, "truncated mp4 not located/recovered")
	}
	res.ParseRoundTrip = float64(res.MP4OK) / float64(res.MP4Trials)
	return nil
}

func runRTP(res *Result) error {
	d := fixture.DisorderedRTP()
	rr := rtppkg.NewReorderer(1, 1, d.Window)
	var got []uint16
	for _, pkt := range d.Packets() {
		r := rr.Push(pkt)
		for _, p := range r.Packets {
			got = append(got, p.SequenceNumber)
		}
	}
	for _, p := range rr.Flush().Packets {
		got = append(got, p.SequenceNumber)
	}
	if len(got) != len(d.Expected) {
		return fmt.Errorf("rtp reorder length = %d want %d", len(got), len(d.Expected))
	}
	for i := range got {
		if got[i] != d.Expected[i] {
			return fmt.Errorf("rtp reorder mismatch at %d: %d != %d", i, got[i], d.Expected[i])
		}
	}
	return nil
}

func runWrap(res *Result) error {
	w := fixture.WrapAround()
	u := timeline.NewUnwrapper(timeline.Boundary{Track: 1, Source: 1, Modulus: w.Modulus})
	for i, raw := range w.Raw {
		abs, err := u.Unwrap(raw)
		if err != nil {
			return fmt.Errorf("unwrap step %d: %w", i, err)
		}
		if abs != w.Expected[i] {
			return fmt.Errorf("unwrap step %d = %d want %d", i, abs, w.Expected[i])
		}
	}
	return nil
}
