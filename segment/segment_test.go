package segment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediapack/model"
)

func TestPlanBoundariesDelaysMissingKeyframe(t *testing.T) {
	keyframes := []int64{0, 2_000, 4_000, 8_000, 10_000, 12_000}
	boundaries, intervals, err := PlanBoundaries(PlanConfig{
		SegmentDurationNanos: 3_000,
		DurationNanos:        12_000,
		Keyframes:            keyframes,
		Track:                "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := BoundaryNanos(boundaries)
	want := []int64{0, 4_000, 8_000, 10_000}
	if len(got) != len(want) {
		t.Fatalf("boundaries=%v intervals=%d", got, len(intervals))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("boundaries=%v want %v", got, want)
		}
	}
	if !boundaries[1].Delayed || boundaries[1].DelayNanos != 1_000 {
		t.Fatalf("3s target should delay 1s: %+v", boundaries[1])
	}
	if len(intervals) != 4 || intervals[0].EndNanos != 4_000 {
		t.Fatalf("unexpected intervals: %+v", intervals)
	}

	other, _, err := PlanBoundaries(PlanConfig{3_000, 12_000, keyframes, "video-low"})
	if err != nil {
		t.Fatal(err)
	}
	if !EqualBoundaries(boundaries, other) {
		t.Fatal("representations must use identical boundaries")
	}
}

func TestPlanBoundariesRequiresZeroKeyframe(t *testing.T) {
	_, _, err := PlanBoundaries(PlanConfig{1_000, 4_000, []int64{1_000, 2_000}, "video"})
	if !model.IsCategory(err, model.CategoryMissingKeyframe) {
		t.Fatalf("expected missing_keyframe, got %v", err)
	}
}

func TestSafePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := SafePath(root, "../evil.json"); !model.IsCategory(err, model.CategoryPathTraversal) {
		t.Fatalf("expected traversal error, got %v", err)
	}
	p, err := SafePath(root, filepath.Join("rep0", "seg0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(root, p)
	if rel != filepath.Join("rep0", "seg0.m4s") {
		t.Fatalf("unexpected safe path: %s", rel)
	}
}

func TestManifestDigestWriteResumeAndTamper(t *testing.T) {
	root := t.TempDir()
	payload := []byte("stub-fragment")
	ref := SegmentRef{
		ID: "r0-0", Track: "video", Rep: "r0", Index: 0,
		StartNanos: 0, EndNanos: 1_000, Path: "r0/seg-0.m4s",
		SizeBytes: len(payload), SHA256: SHA256Bytes(payload),
	}
	m := &Manifest{
		Version: ManifestVersion, Seed: 17, Timebase: "1/1000000000",
		DurationNanos: 1_000, ManifestSHA256: "must-be-overwritten",
		Representations: []RepresentationManifest{{ID: "r0", Segments: []SegmentRef{ref}}},
	}
	path, err := WriteManifest(root, m)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "manifest.json" || m.ManifestSHA256 == "must-be-overwritten" {
		t.Fatalf("manifest checksum not filled: %+v", m)
	}
	if _, err := WriteFileSafe(root, ref.Path, payload); err != nil {
		t.Fatal(err)
	}

	read, digest, err := ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if digest != m.ManifestSHA256 {
		t.Fatalf("digest mismatch after read: %s vs %s", digest, m.ManifestSHA256)
	}
	verification, err := VerifyAllSegments(root, read)
	if err != nil || verification.VerifiedSegments != 1 || len(verification.MissingSegments) != 0 {
		t.Fatalf("expected verified segment: %+v err=%v", verification, err)
	}

	// Missing segment is reported for regeneration, not a digest rejection.
	if err := os.Remove(filepath.Join(root, ref.Path)); err != nil {
		t.Fatal(err)
	}
	verification, err = VerifyAllSegments(root, read)
	if err != nil || verification.VerifiedSegments != 0 || len(verification.MissingSegments) != 1 {
		t.Fatalf("expected missing segment: %+v err=%v", verification, err)
	}

	// An existing but modified segment must reject resume.
	if _, err := WriteFileSafe(root, ref.Path, []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	_, err = VerifyAllSegments(root, read)
	if !model.IsCategory(err, model.CategoryChecksumMismatch) {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestManifestJSONStable(t *testing.T) {
	newManifest := func() *Manifest {
		return &Manifest{Version: 1, Seed: 17, Timebase: CommonNanosForTest(), Boundaries: []Boundary{{Index: 0, Nanos: 0}}}
	}
	a := newManifest()
	b := newManifest()
	da, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db, _ := b.Digest()
	if da != db {
		t.Fatal("identical manifests produced different digests")
	}
	data, _ := json.Marshal(a)
	if len(data) == 0 {
		t.Fatal("json marshal returned empty bytes")
	}
}

func CommonNanosForTest() string { return "1/1000000000" }
