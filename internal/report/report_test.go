package report_test

import (
	"bytes"
	"strings"
	"testing"

	"medtrace/internal/core"
	"medtrace/internal/report"
	"medtrace/internal/stream"
)

// sampleIndex builds a two-segment index with fixed digests.
func sampleIndex() *core.Index {
	mk := func(uri, hash string, start, end core.Nanos, epoch int) *core.Segment {
		return &core.Segment{
			URI: uri, SHA256: hash, Size: 100,
			Start: start, End: end, Epoch: epoch,
			Track: core.Track{Kind: core.Video, ID: 1},
		}
	}
	idx := core.NewIndex([]*core.Segment{
		mk("a/seg-1.m4s", strings.Repeat("a", 64), 0, core.Second, 0),
		mk("a/seg-2.m4s", strings.Repeat("b", 64), core.Second, 2*core.Second, 0),
	})
	idx.Epochs = []core.Epoch{{Number: 0, Reason: core.EpochStart, Source: "a/seg-1.m4s"}}
	return idx
}

func sampleResult() *stream.Result {
	return &stream.Result{
		HashChecks: 2, HashMatches: 2,
		KeyframeHits: 1, KeyframeTries: 1,
		Anomalies: []core.Anomaly{{
			Category: core.CatHashMismatch, Segment: "a/seg-1.m4s",
			Pos: core.Pos{File: "a/seg-1.m4s", Byte: 12}, ByteOffset: 12,
			Expected: strings.Repeat("a", 64), Actual: strings.Repeat("f", 64),
		}},
		Recoveries: []core.Recovery{{
			FailedSegment: "a/seg-1.m4s", FailedByteOffset: 12,
			UnrecoveredStart: 0, UnrecoveredEnd: core.Second,
			Resumed: true, ResumeSegment: "a/seg-2.m4s",
			ResumeByteOffset: 40, ResumeCanonical: core.Second,
		}},
	}
}

func TestBuildDigestAndWriteAreStable(t *testing.T) {
	idx := sampleIndex()
	build := func() *report.Report {
		return report.Build(0, 2*core.Second, idx, idx.Epochs, nil, sampleResult())
	}
	r1, r2 := build(), build()
	d1, err := r1.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	d2, err := r2.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if d1 != d2 {
		t.Errorf("identical inputs produced different digests: %s vs %s", d1, d2)
	}
	if r1.Stats.Anomalies != 1 || r1.Stats.SegmentHashes != 2 || r1.Stats.KeyframeHit != 1 {
		t.Errorf("stats = %+v", r1.Stats)
	}
	if len(r1.Hashes) != 2 || r1.Hashes[0].SHA256 != strings.Repeat("a", 64) {
		t.Errorf("hashes = %+v", r1.Hashes)
	}
	var buf bytes.Buffer
	if err := r1.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"anomalies (1):", "segment hashes (2):", "recoveries (1):",
		"hash_mismatch=", "report_sha256=" + d1,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// The anomaly record keeps the original offset and both digests.
	a := r1.Anomalies[0]
	if a.ByteOffset != 12 || a.Expected == "" || a.Actual == "" {
		t.Errorf("anomaly = %+v", a)
	}
}

func TestRecountDerivesCountersFromContent(t *testing.T) {
	r := report.Build(0, 2*core.Second, sampleIndex(), nil, nil, sampleResult())
	r.Recount()
	s := r.Stats
	if s.HashMismatch != 1 || s.Recovered != 1 || s.OffsetPreserved != 1 {
		t.Errorf("stats = %+v", s)
	}
	// A resume that lands on the failed offset does not count as preserved.
	r.Recoveries[0].ResumeByteOffset = r.Recoveries[0].FailedByteOffset
	r.Recount()
	if r.Stats.OffsetPreserved != 0 {
		t.Errorf("offset preserved = %d, want 0", r.Stats.OffsetPreserved)
	}
	// An unfinished recovery counts neither as recovered nor preserved.
	r.Recoveries[0].Resumed = false
	r.Recount()
	if r.Stats.Recovered != 0 {
		t.Errorf("recovered = %d, want 0", r.Stats.Recovered)
	}
}

func TestMergeRenumbersEpochsAndSpansInterval(t *testing.T) {
	r1 := report.Build(core.Second, 2*core.Second, sampleIndex(), []core.Epoch{
		{Number: 0, Reason: core.EpochStart, Source: "a/seg-1.m4s"},
	}, nil, sampleResult())
	other := report.Build(0, 3*core.Second, sampleIndex(), []core.Epoch{
		{Number: 0, Reason: core.EpochStart, Source: "b/seg-1.m4s"},
		{Number: 1, Reason: core.EpochWrap, Source: "b/seg-2.m4s", RawBase: 200},
	}, []core.Anomaly{{
		Category: core.CatTimeJump, Reason: string(core.EpochWrap),
		Segment: "b/seg-2.m4s", ByteOffset: 7, Canonical: 3 * core.Second / 2,
	}}, &stream.Result{HashChecks: 4, HashMatches: 4, KeyframeTries: 2})

	r1.Merge(other)

	if r1.FromNS != 0 || r1.ToNS != int64(3*core.Second) {
		t.Errorf("interval = [%d,%d)", r1.FromNS, r1.ToNS)
	}
	if len(r1.Epochs) != 3 {
		t.Fatalf("epochs = %d, want 3", len(r1.Epochs))
	}
	for i, e := range r1.Epochs {
		if e.Number != i {
			t.Errorf("epoch %d renumbered to %d", i, e.Number)
		}
	}
	if r1.Stats.SegmentHashes != 6 || r1.Stats.KeyframeTotal != 3 {
		t.Errorf("stats = %+v", r1.Stats)
	}
	// Anomalies from both inputs are merged in canonical order.
	if len(r1.Anomalies) != 2 {
		t.Fatalf("anomalies = %d, want 2", len(r1.Anomalies))
	}
	if r1.Anomalies[0].CanonicalNS > r1.Anomalies[1].CanonicalNS {
		t.Error("merged anomalies are not in canonical order")
	}
	if r1.Stats.Anomalies != 2 || r1.Stats.HashMismatch != 1 {
		t.Errorf("recount after merge = %+v", r1.Stats)
	}
}

func TestSortedHashLinesIsOrderIndependent(t *testing.T) {
	r := report.Build(0, core.Second, sampleIndex(), nil, nil, &stream.Result{})
	lines := report.SortedHashLines(r)
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0] >= lines[1] {
		t.Errorf("lines not sorted: %v", lines)
	}
	if !strings.HasPrefix(lines[0], "a/seg-1.m4s "+strings.Repeat("a", 64)) {
		t.Errorf("line = %q", lines[0])
	}
}
