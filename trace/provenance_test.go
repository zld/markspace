package trace_test

import (
	"testing"

	"github.com/zld/mediapipe/trace"
)

func segs() []trace.SegmentTrace {
	return []trace.SegmentTrace{
		{Number: 2, Layer: "reference", OutputPath: "reference-seg002.bin", OutputSHA256: "ccc"},
		{Number: 1, Layer: "reference", OutputPath: "reference-seg001.bin", OutputSHA256: "aaa"},
		{Number: 1, Layer: "video-500k", OutputPath: "video-500k-seg001.bin", OutputSHA256: "bbb"},
	}
}

func build(t *testing.T) *trace.Provenance {
	t.Helper()
	p, err := trace.Build("run-1",
		[]trace.Source{{Role: "media", Path: "in.mp4", SHA256: "in", Index: "idx"}},
		segs(), "params", 1)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProvenanceRecordsInputsAndOutputs(t *testing.T) {
	p := build(t)
	if len(p.Sources) != 1 || p.Sources[0].SHA256 != "in" {
		t.Fatalf("sources=%+v", p.Sources)
	}
	// Segments are canonicalized by number then layer.
	want := []string{"reference-seg001.bin", "video-500k-seg001.bin", "reference-seg002.bin"}
	for i, s := range p.Segments {
		if s.OutputPath != want[i] {
			t.Fatalf("segment %d = %s want %s", i, s.OutputPath, want[i])
		}
		if s.IndexDigest != "" {
			t.Fatal("segment trace must not invent an index digest")
		}
	}
}

func TestProvenanceDigestDeterministic(t *testing.T) {
	a := build(t)
	b := build(t)
	if a.Digest != b.Digest {
		t.Fatal("digest not deterministic")
	}
}

func TestProvenanceDigestChangesWithOutput(t *testing.T) {
	a := build(t)
	changed := segs()
	changed[0].OutputSHA256 = "zzz"
	p, err := trace.Build("run-1",
		[]trace.Source{{Role: "media", Path: "in.mp4", SHA256: "in", Index: "idx"}},
		changed, "params", 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Digest == a.Digest {
		t.Fatal("digest must change with output content")
	}
}

func TestProvenanceCanonicalJSON(t *testing.T) {
	p := build(t)
	b, err := p.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty canonical JSON")
	}
}
