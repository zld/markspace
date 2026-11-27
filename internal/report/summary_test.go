package report

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/maptrack"
	"mediapack/internal/model"
	"mediapack/internal/remux"
	"mediapack/internal/timeline"
)

func sampleMapping() *maptrack.Mapping {
	b := &maptrack.Binding{
		TargetTrack: 7, SourcePath: "a.mxf", SourceTrack: 1,
		Format: "mxf", Codec: "vraw",
		Meta: model.TrackMeta{TrackID: 7},
		Decisions: []maptrack.Decision{{
			Field: "rotation",
			Adopted: model.Field[any]{
				Value: model.Rotation{Degrees: 90}, Source: model.SourceMXF,
				Raw: "90",
			},
			Candidates: []model.Field[any]{{
				Value: model.Rotation{Degrees: 90}, Source: model.SourceMXF,
				Raw: "90",
			}},
		}},
	}
	for i := 0; i < 3; i++ {
		b.Samples = append(b.Samples, model.Sample{
			Source: "a.mxf", SourceTrack: 1, TargetTrack: 7,
			Index: uint32(i), SrcOffset: int64(100 + i*40),
			Size: 10, Payload: []byte("0123456789"),
		})
	}
	return &maptrack.Mapping{Bindings: []*maptrack.Binding{b}}
}

func TestBuildAggregatesMappingAndTimeline(t *testing.T) {
	tl := timeline.New(timeline.StandardRule(), 8)
	events := []timeline.Event{
		{Seq: 1, Source: "a", TrackID: 7, SampleIndex: 0, OriginalTick: 100, Duration: 10},
		{Seq: 2, Source: "a", TrackID: 7, SampleIndex: 1, OriginalTick: 50, Duration: 10},
	}
	for _, e := range events {
		if _, err := tl.Ingest(e); err != nil {
			t.Fatal(err)
		}
	}
	s := Build(Inputs{Mapping: sampleMapping(), Timeline: tl})
	if s.Status != "ok" || s.TrackCount != 1 || len(s.Samples) != 3 {
		t.Fatalf("summary wrong: %+v", s)
	}
	if len(s.MetadataDecisions) != 1 ||
		s.MetadataDecisions[0].Adopted.Source != model.SourceMXF {
		t.Fatalf("decision missing: %+v", s.MetadataDecisions)
	}
	// Second event was clamped: delta = 101-50 = 51.
	if s.MaxTimeDeltaTick != 51 {
		t.Fatalf("max delta = %d want 51", s.MaxTimeDeltaTick)
	}
	if len(s.Boundaries) != 2 {
		t.Fatalf("boundaries %d", len(s.Boundaries))
	}
}

func TestSHA256StableAndManifestOffsets(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.mpkm")
	m := sampleMapping()
	mf, err := remux.Write(m, out)
	if err != nil {
		t.Fatal(err)
	}
	s1 := Build(Inputs{Mapping: m, Manifest: mf, OutputPath: out})
	s1.AttachManifestOffsets(mf)
	s2 := Build(Inputs{Mapping: m, Manifest: mf, OutputPath: out})
	s2.AttachManifestOffsets(mf)
	if s1.OutputSHA256 == "" || s1.OutputSHA256 != s2.OutputSHA256 {
		t.Fatal("SHA-256 missing or unstable")
	}
	if s1.ByteDiff != 0 || s1.SampleCount != 3 {
		t.Fatalf("manifest aggregate wrong: %+v", s1)
	}
	for _, line := range s1.Samples {
		if line.TargetOffset <= 0 {
			t.Fatalf("target offset not attached: %+v", line)
		}
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output missing: %v", err)
	}
}

func TestFailureReportedWithCodeAndOffset(t *testing.T) {
	err := model.NewError(model.CodeTruncatedBox, "box cut short").
		WithFile("bad.mxf").WithOffset(4242).WithTrack(3)
	s := Build(Inputs{Err: err})
	if s.Status != "error" || s.Failure == nil {
		t.Fatal("failure not reported")
	}
	if s.Failure.Code != int(model.CodeTruncatedBox) ||
		s.Failure.Offset != 4242 || s.Failure.Track != 3 ||
		s.Failure.File != "bad.mxf" {
		t.Fatalf("failure detail wrong: %+v", s.Failure)
	}
}

func TestDeterministicSummaryOrdering(t *testing.T) {
	m := sampleMapping()
	a := Build(Inputs{Mapping: m})
	b := Build(Inputs{Mapping: m})
	if len(a.Samples) != len(b.Samples) {
		t.Fatal("sample count differs")
	}
	for i := range a.Samples {
		if a.Samples[i] != b.Samples[i] {
			t.Fatalf("non-deterministic sample line at %d", i)
		}
	}
}
