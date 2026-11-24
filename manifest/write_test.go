package manifest_test

import (
	"encoding/json"
	"testing"

	"github.com/zld/mediapipe/manifest"
	"github.com/zld/mediapipe/media"
	"github.com/zld/mediapipe/pipeline"
)

func planned() []pipeline.PlannedSegment {
	return []pipeline.PlannedSegment{
		{Number: 1, StartUS: 0, EndUS: 2_000_000, MaxDeviationUS: 0},
		{Number: 2, StartUS: 2_000_000, EndUS: 4_000_000, MaxDeviationUS: 33_333},
	}
}

func outs() []pipeline.SegmentOutput {
	return []pipeline.SegmentOutput{
		{Number: 1, Layer: "reference", StartUS: 0, EndUS: 2_000_000, Path: "reference-seg001.bin", SHA256: "aaa", Bytes: 10, Samples: 60},
		{Number: 1, Layer: "video-500k", StartUS: 0, EndUS: 2_000_000, Path: "video-500k-seg001.bin", SHA256: "bbb", Bytes: 10, Samples: 60},
		{Number: 2, Layer: "reference", StartUS: 2_000_000, EndUS: 4_000_000, Path: "reference-seg002.bin", SHA256: "ccc", Bytes: 10, Samples: 60},
		{Number: 2, Layer: "video-500k", StartUS: 2_000_000, EndUS: 4_000_000, Path: "video-500k-seg002.bin", SHA256: "ddd", Bytes: 10, Samples: 60},
	}
}

func build(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Build("inputsha", "idxsha", "paramsha", 1,
		2*media.Time(media.NanoDen), planned(), outs(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestOrderIsCanonical(t *testing.T) {
	m := build(t)
	// Entries arrive sorted by segment then layer, regardless of input order.
	wantPaths := []string{"reference-seg001.bin", "video-500k-seg001.bin", "reference-seg002.bin", "video-500k-seg002.bin"}
	for i, e := range m.Entries {
		if e.Path != wantPaths[i] {
			t.Fatalf("entry %d = %s want %s", i, e.Path, wantPaths[i])
		}
	}
	if m.SegmentCount != 2 {
		t.Fatalf("segment count=%d want 2", m.SegmentCount)
	}
}

func TestManifestDigestIsDeterministic(t *testing.T) {
	a := build(t)
	b := build(t)
	if a.Digest != b.Digest {
		t.Fatal("digest not deterministic")
	}
	// Reversing the input output order must not change the digest.
	reversed := outs()
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	m, err := manifest.Build("inputsha", "idxsha", "paramsha", 1,
		2*media.Time(media.NanoDen), planned(), reversed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Digest != a.Digest {
		t.Fatal("digest depends on execution order")
	}
}

func TestManifestDigestChangesWithContent(t *testing.T) {
	a := build(t)
	changed := outs()
	changed[0].SHA256 = "zzz"
	m, err := manifest.Build("inputsha", "idxsha", "paramsha", 1,
		2*media.Time(media.NanoDen), planned(), changed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Digest == a.Digest {
		t.Fatal("digest must change with segment content")
	}
}

func TestManifestRejectsDuplicateEntries(t *testing.T) {
	dupes := append(outs(), outs()[0])
	if _, err := manifest.Build("i", "x", "p", 1, 2*media.Time(media.NanoDen), planned(), dupes, nil); err == nil {
		t.Fatal("duplicate entries should fail")
	}
}

func TestParamsDigestTracksParameters(t *testing.T) {
	a := manifest.ParamsDigest(2*media.Time(media.NanoDen), []string{"500k", "1000k"}, 40*media.Time(media.NanoDen))
	b := manifest.ParamsDigest(2*media.Time(media.NanoDen), []string{"500k", "1000k"}, 40*media.Time(media.NanoDen))
	if a != b {
		t.Fatal("params digest not deterministic")
	}
	c := manifest.ParamsDigest(3*media.Time(media.NanoDen), []string{"500k"}, 40*media.Time(media.NanoDen))
	if a == c {
		t.Fatal("params digest must change with parameters")
	}
}

func TestManifestJSONRoundTrips(t *testing.T) {
	m := build(t)
	b, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	var back manifest.Manifest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Digest != m.Digest || back.SegmentCount != m.SegmentCount {
		t.Fatal("round trip lost fields")
	}
}
