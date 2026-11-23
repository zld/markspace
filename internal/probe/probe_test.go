package probe

import (
	"bytes"
	"strings"
	"testing"

	"github.com/broadcast/mediad/internal/fixture"
	"github.com/broadcast/mediad/internal/model"
)

func probeBytes(t *testing.T, raw []byte) (*model.Graph, *model.ErrorSet) {
	t.Helper()
	errs := &model.ErrorSet{}
	r := bytes.NewReader(raw)
	g, _, err := Probe(r, int64(len(raw)), DefaultConfig(), errs)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	return g, errs
}

func TestNormalFixtureParsesTwoTracks(t *testing.T) {
	spec := fixture.StandardSet(23)[0]
	raw := fixture.MustGenerate(spec)
	g, errs := probeBytes(t, raw)

	if len(g.IDs()) != 2 {
		t.Fatalf("tracks = %d, want 2", len(g.IDs()))
	}
	v := g.Tracks[1]
	a := g.Tracks[2]
	if v.Kind != model.KindVideo || a.Kind != model.KindAudio {
		t.Fatalf("kinds = %s/%s", v.Kind, a.Kind)
	}
	if v.SampleCount() != 48 || a.SampleCount() != 96 {
		t.Fatalf("sample counts = %d/%d", v.SampleCount(), a.SampleCount())
	}
	if v.Timescale != 24000 || a.Timescale != 48000 {
		t.Fatalf("timescales = %d/%d", v.Timescale, a.Timescale)
	}
	// First keyframe must be sample 0; every 12th sample marked.
	if !v.Samples[0].IsKeyframe || !v.Samples[12].IsKeyframe || v.Samples[1].IsKeyframe {
		t.Fatal("keyframe index not reconstructed from stss")
	}
	if errs.Count() != 0 {
		t.Fatalf("normal fixture must be clean, got %v", errs.All())
	}
	// Sample offsets must point inside the file.
	for _, s := range v.Samples {
		if s.Offset == 0 || int64(s.Offset)+int64(s.Size) > int64(len(raw)) {
			t.Fatalf("sample offset %d size %d outside file %d", s.Offset, s.Size, len(raw))
		}
	}
}

func TestTruncatedMdatIsReportedNotPanicked(t *testing.T) {
	spec := fixture.StandardSet(23)[1]
	if spec.Corruption != fixture.CorruptTruncatedMdat {
		t.Fatal("fixture order changed; update test index")
	}
	raw := fixture.MustGenerate(spec)
	errs := &model.ErrorSet{}
	r := bytes.NewReader(raw)
	if _, err := ScanBoxes(r, int64(len(raw)), errs); err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if !errs.Has(model.KindTruncatedBox) {
		t.Fatalf("expected truncated_box, got %v", errs.All())
	}
}

func TestBackwardTimestampsDetected(t *testing.T) {
	spec := fixture.StandardSet(23)[2]
	raw := fixture.MustGenerate(spec)
	_, errs := probeBytes(t, raw)
	if !errs.Has(model.KindTimestampDuplicate) {
		t.Fatalf("expected duplicate timestamp from backward variant, got %v", errs.All())
	}
}

func TestSparseKeyframesAndMissingIndex(t *testing.T) {
	spec := fixture.StandardSet(23)[3]
	raw := fixture.MustGenerate(spec)
	g, errs := probeBytes(t, raw)
	v := g.Tracks[1]
	if !errs.Has(model.KindSparseKeyframe) {
		t.Fatalf("expected sparse_keyframe, got %v", errs.All())
	}
	// Only first and last samples remain keyframes.
	marked := 0
	for _, s := range v.Samples {
		if s.IsKeyframe {
			marked++
		}
	}
	if marked != 2 {
		t.Fatalf("keyframe count = %d, want 2", marked)
	}

	// A video track without stss must produce a missing-index warning and no
	// keyframe claims beyond what the sample table proves.
	noStss := spec
	noStss.Corruption = fixture.CorruptNone
	noStss.Tracks[0].KeyframeEvery = 0
	raw2 := fixture.MustGenerate(noStss)
	g2, errs2 := probeBytes(t, raw2)
	if !errs2.Has(model.KindMissingKeyframeIndex) {
		t.Fatalf("expected missing_keyframe_index, got %v", errs2.All())
	}
	for _, s := range g2.Tracks[1].Samples {
		if s.IsKeyframe {
			t.Fatal("no keyframe may be claimed without stss")
		}
	}
}

func TestMetadataConflictsAreExplicit(t *testing.T) {
	set := fixture.StandardSet(23)
	cases := map[int]model.ErrorKind{
		5: model.KindChannelLayoutConflict,
		6: model.KindSampleRateConflict,
		7: model.KindColorConflict,
	}
	for idx, kind := range cases {
		raw := fixture.MustGenerate(set[idx])
		_, errs := probeBytes(t, raw)
		if !errs.Has(kind) {
			t.Fatalf("fixture %s: expected %s, got %v", set[idx].Name, kind, errs.All())
		}
		for _, e := range errs.ByTrack(2) {
			if e.Kind == kind && !strings.Contains(e.Detail, "policy=") {
				t.Fatalf("conflict %s missing explicit policy: %s", kind, e.Detail)
			}
		}
	}
}

func TestNonZeroStartTimeline(t *testing.T) {
	spec := fixture.StandardSet(23)[4]
	raw := fixture.MustGenerate(spec)
	g, _ := probeBytes(t, raw)
	v := g.Tracks[1]
	if v.StartTime.Cmp(model.MustRat(0, 1)) <= 0 {
		t.Fatalf("non-zero start expected, got %s", v.StartTime)
	}
	// 12000 units / 24000 timescale = 0.5 s leading offset.
	if v.StartTime.Cmp(model.MustRat(1, 2)) != 0 {
		t.Fatalf("start = %s, want 1/2", v.StartTime)
	}
}

func TestSafePathRejectsTraversal(t *testing.T) {
	for _, p := range []string{"../etc/passwd", "/abs/path", "a/../../b"} {
		if _, err := SafePath("/tmp/base", p); err == nil {
			t.Fatalf("path %q should be rejected", p)
		}
	}
	clean, err := SafePath("/tmp/base", "in/file.mp4")
	if err != nil || clean != "/tmp/base/in/file.mp4" {
		t.Fatalf("safe path = %q, err=%v", clean, err)
	}
}
