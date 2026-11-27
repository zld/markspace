package manifest

import (
	"testing"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
)

func sampleManifest(job, inputDigest string) *Manifest {
	m := &Manifest{
		JobID: job, Timescale: clock.TicksPerSecond,
		Codecs:      []string{"h264", "av1"},
		Bitrates:    []int{500, 1000},
		InputDigest: inputDigest,
		Entries: []Entry{
			{
				ID: "seg-0000", Index: 0, StartTick: 0, EndTick: 9000,
				Segments: map[string]Fragment{
					"h264-1000": {Path: "a/0.mp4", Size: 40, SampleCount: 3, SHA256: "aa", TickRange: [2]clock.Tick{0, 9000}},
					"av1-500":   {Path: "b/0.mp4", Size: 30, SampleCount: 3, SHA256: "bb", TickRange: [2]clock.Tick{0, 9000}},
				},
				OrigOffsets: map[string]int64{"h264-1000": 12, "av1-500": 8},
			},
			{
				ID: "seg-0001", Index: 1, StartTick: 9000, EndTick: 18000,
				Segments: map[string]Fragment{
					"h264-1000": {Path: "a/1.mp4", Size: 42, SampleCount: 3, SHA256: "cc", TickRange: [2]clock.Tick{9000, 18000}},
					"av1-500":   {Path: "b/1.mp4", Size: 31, SampleCount: 3, SHA256: "dd", TickRange: [2]clock.Tick{9000, 18000}},
				},
				OrigOffsets: map[string]int64{"h264-1000": 60, "av1-500": 40},
			},
		},
	}
	return m
}

func TestDeterministicDigest(t *testing.T) {
	a := sampleManifest("job-1", "input-x")
	b := sampleManifest("job-1", "input-x")
	da, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest(b)
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatal("identical manifests must hash identically")
	}
	// Map insertion order must not affect the digest.
	reorder := sampleManifest("job-1", "input-x")
	reorder.Entries[0].Segments = map[string]Fragment{
		"av1-500":   a.Entries[0].Segments["av1-500"],
		"h264-1000": a.Entries[0].Segments["h264-1000"],
	}
	dr, _ := Digest(reorder)
	if dr != da {
		t.Fatal("map key ordering changed the manifest digest")
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	m := sampleManifest("job-2", "in")
	b, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(got); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	m := sampleManifest("job-3", "in")
	b, _ := Marshal(m)
	got, _ := Unmarshal(b)
	// Tamper with a fragment hash inside an entry.
	got.Entries[0].Segments["h264-1000"] = Fragment{
		Path: "a/0.mp4", Size: 99, SampleCount: 3, SHA256: "zz",
	}
	err := Verify(got)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeChecksum {
		t.Fatalf("want CHECKSUM_MISMATCH, got %v", err)
	}
}

func TestCompareFirstRunAllNew(t *testing.T) {
	m := sampleManifest("job", "in")
	d, err := Compare(nil, m)
	if err != nil {
		t.Fatal(err)
	}
	if d.NewCount != 2 || d.ReusedCount != 0 || d.ChangedCount != 0 {
		t.Fatalf("first run diff = %+v", d)
	}
}

func TestCompareIdenticalAllReused(t *testing.T) {
	prev := sampleManifest("job", "in")
	cand := sampleManifest("job", "in")
	d, _ := Compare(prev, cand)
	if d.ReusedCount != 2 || d.NewCount != 0 || d.ChangedCount != 0 {
		t.Fatalf("identical rerun diff = %+v", d)
	}
}

func TestCompareInputChangeNoReuse(t *testing.T) {
	prev := sampleManifest("job", "in-a")
	cand := sampleManifest("job", "in-b") // same entries, different input
	d, _ := Compare(prev, cand)
	if d.InputChanged != true || d.ReusedCount != 0 || d.ChangedCount != 2 {
		t.Fatalf("input change must block reuse: %+v", d)
	}
}

func TestCompareNewAndChanged(t *testing.T) {
	prev := sampleManifest("job", "in")
	cand := sampleManifest("job", "in")
	// Drop one entry, alter another's fragment.
	cand.Entries = cand.Entries[:1]
	cand.Entries[0].Segments["h264-1000"] = Fragment{
		Path: "a/0.mp4", Size: 77, SampleCount: 3, SHA256: "new",
	}
	d, _ := Compare(prev, cand)
	if d.ChangedCount != 1 || len(d.ChangedIDs) != 1 || d.ChangedIDs[0] != "seg-0000" {
		t.Fatalf("changed detection wrong: %+v", d)
	}
}
