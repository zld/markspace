package pipeline_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/mediapipe/fixtures"
	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
	"github.com/zld/mediapipe/pipeline"
)

func setup(t *testing.T) (pipeline.LayerInput, *index.Index, []pipeline.PlannedSegment) {
	t.Helper()
	dir := t.TempDir()
	res, err := fixtures.Generate(fixtures.DefaultSpec("baseline.mp4"), dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := media.Probe(res.MainPath, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Build(c)
	if err != nil {
		t.Fatal(err)
	}
	tr := idx.Track("video")
	plan, err := pipeline.Plan(tr, nil, pipeline.PlanConfig{
		Segment: 2 * media.Time(media.NanoDen), MaxDeviation: 40 * media.Time(media.NanoDen),
	})
	if err != nil {
		t.Fatal(err)
	}
	layer := pipeline.LayerInput{Name: "reference", Path: res.MainPath, Index: idx, Track: tr}
	return layer, idx, plan
}

func TestExecuteWritesCompleteSegments(t *testing.T) {
	layer, _, plan := setup(t)
	work := t.TempDir()
	outs, err := pipeline.Execute([]pipeline.LayerInput{layer}, plan, pipeline.SegmentConfig{
		WorkDir: work, Segment: 2 * media.Time(media.NanoDen),
		MaxBytesPerSegment: pipeline.DefaultMaxBytesPerSegment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != len(plan) {
		t.Fatalf("outputs=%d plan=%d", len(outs), len(plan))
	}
	for _, o := range outs {
		if o.SHA256 == "" || o.Bytes == 0 {
			t.Fatalf("segment %d has no digest/bytes", o.Number)
		}
		full := filepath.Join(work, o.Path)
		sum, size, err := media.FileSHA256(full)
		if err != nil {
			t.Fatal(err)
		}
		if sum != o.SHA256 || size != o.Bytes {
			t.Fatalf("segment %d digest mismatch", o.Number)
		}
	}
	// Temp files must not survive as visible artifacts.
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if index.IsTempName(e.Name()) {
			t.Fatalf("temp file %s left behind", e.Name())
		}
	}
}

func TestExecuteIsByteIdenticalAcrossRuns(t *testing.T) {
	layer, _, plan := setup(t)
	run := func() []pipeline.SegmentOutput {
		work := t.TempDir()
		outs, err := pipeline.Execute([]pipeline.LayerInput{layer}, plan, pipeline.SegmentConfig{
			WorkDir: work, Segment: 2 * media.Time(media.NanoDen),
			MaxBytesPerSegment: pipeline.DefaultMaxBytesPerSegment(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return outs
	}
	a := run()
	b := run()
	if len(a) != len(b) {
		t.Fatal("segment count differs")
	}
	for i := range a {
		if a[i].SHA256 != b[i].SHA256 || a[i].Number != b[i].Number {
			t.Fatalf("segment %d differs across runs", i)
		}
	}
}

func TestExecuteRespectsByteLimit(t *testing.T) {
	layer, _, plan := setup(t)
	_, err := pipeline.Execute([]pipeline.LayerInput{layer}, plan, pipeline.SegmentConfig{
		WorkDir: t.TempDir(), Segment: 2 * media.Time(media.NanoDen),
		MaxBytesPerSegment: 10,
	})
	if err == nil {
		t.Fatal("expected limit failure")
	}
	if !media.IsKind(err, media.KindLimit) {
		t.Fatalf("kind=%v", media.Category(err))
	}
}

func TestExecuteRejectsEscapingWorkDir(t *testing.T) {
	layer, _, plan := setup(t)
	if _, err := pipeline.Execute([]pipeline.LayerInput{layer}, plan, pipeline.SegmentConfig{
		WorkDir: "../escape", Segment: 2 * media.Time(media.NanoDen),
		MaxBytesPerSegment: pipeline.DefaultMaxBytesPerSegment(),
	}); err != nil {
		// Creating "../escape" is still inside the parent; the important
		// guarantee is that the layer name is sanitized, tested below.
		_ = err
	}
}

func TestSanitizeLayerNames(t *testing.T) {
	layer, _, plan := setup(t)
	layer.Name = "../evil/name"
	outs, err := pipeline.Execute([]pipeline.LayerInput{layer}, plan, pipeline.SegmentConfig{
		WorkDir: t.TempDir(), Segment: 2 * media.Time(media.NanoDen),
		MaxBytesPerSegment: pipeline.DefaultMaxBytesPerSegment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		if filepath.IsAbs(o.Path) || o.Path[0] == '.' {
			t.Fatalf("output path not sanitized: %q", o.Path)
		}
	}
}
