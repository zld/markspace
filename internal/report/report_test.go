package report

import (
	"encoding/json"
	"strings"
	"testing"

	"mediakernel/internal/clock"
)

func TestBuildAndCanonicalStability(t *testing.T) {
	o := Options{
		Frames: 96000, IndexEntries: 200, InputDigest: "a", IndexDigest: "b",
		OutputSHA256: strings.Repeat("c", 64), FirstByteOffset: -1,
	}
	r1 := Build(o)
	if r1.Status != StatusOK || r1.LocatingErrors != 0 {
		t.Fatalf("clean report wrong: %+v", r1)
	}
	j1, err := r1.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	j2, _ := Build(o).CanonicalJSON()
	if string(j1) != string(j2) {
		t.Fatal("canonical JSON must be byte-stable")
	}
	var parsed map[string]any
	if err := json.Unmarshal(j1, &parsed); err != nil {
		t.Fatalf("report not valid JSON: %v", err)
	}
	if parsed["frames"].(float64) != 96000 || parsed["schema_version"] != SchemaVersion {
		t.Fatal("report fields missing/wrong")
	}
	d1, _ := r1.Digest()
	d2, _ := Build(o).Digest()
	if d1 != d2 || len(d1) != 64 {
		t.Fatal("report digest must be stable sha-256 hex")
	}
}

func TestFaultedStatusAndOffset(t *testing.T) {
	base := Options{Frames: 10, OutputSHA256: "x"}
	locErr := clock.NewError("wav", clock.CatTruncated, "cut").At(4096)
	r := FromError(base, locErr)
	if r.Status != StatusFaulted || r.LocatingErrors != 1 || r.FirstByteOffset != 4096 {
		t.Fatalf("faulted report wrong: %+v", r)
	}
	// Non-located error still marks faulted but leaves the offset absent.
	r2 := FromError(base, clock.NewError("wav", clock.CatUnsupported, "x"))
	if r2.Status != StatusFaulted || r2.FirstByteOffset != -1 {
		t.Fatalf("non-located fault report wrong: %+v", r2)
	}
}

func TestResumeFieldsRendered(t *testing.T) {
	r := Build(Options{Frames: 100, Resume: true, ResumeMillis: 12, ReusedStages: 3,
		OutputSHA256: "d"})
	raw, err := r.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"resume": true`) || !strings.Contains(s, `"resume_ms": 12`) {
		t.Fatalf("resume fields missing: %s", s)
	}
}
