package pipeline

import (
	"testing"

	"github.com/zld/media-check/media"
)

func TestMergerIsOrderIndependent(t *testing.T) {
	const n = 20
	build := func(order []int) *Manifest {
		merger, err := NewMerger(n, "src", "deadbeef", 7)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range order {
			res := Result{
				Index: i, TrackID: uint32(i%2 + 1), Status: StatusOK,
				Segment: media.Segment{Index: i, First: uint32(i), Count: 1, DTSStart: uint64(i), Duration: 1, ByteSize: int64(i)},
				SHA256:  "sha" + string(rune('a'+i)),
			}
			if err := merger.Add(res); err != nil {
				t.Fatal(err)
			}
		}
		man, err := merger.Build(Manifest{Tracks: 2})
		if err != nil {
			t.Fatal(err)
		}
		return man
	}
	forward := make([]int, n)
	reverse := make([]int, n)
	for i := 0; i < n; i++ {
		forward[i] = i
		reverse[i] = n - 1 - i
	}
	a, b := build(forward), build(reverse)
	if a.SHA256 != b.SHA256 {
		t.Fatalf("manifest digest depends on completion order: %s != %s", a.SHA256, b.SHA256)
	}
	for i := range a.Entries {
		if a.Entries[i].Index != i {
			t.Fatalf("entry %d has index %d", i, a.Entries[i].Index)
		}
	}
	// A duplicate result must be rejected, not overwrite a success.
	merger, _ := NewMerger(2, "s", "h", 1)
	if err := merger.Add(Result{Index: 0, Status: StatusOK}); err != nil {
		t.Fatal(err)
	}
	if err := merger.Add(Result{Index: 0, Status: StatusFailed}); err == nil {
		t.Fatal("duplicate index accepted")
	}
}

func TestManifestInvariantsAndFailuresIsolated(t *testing.T) {
	merger, _ := NewMerger(4, "s", "h", 1)
	segs := []media.Segment{
		{Index: 0, TrackID: 1, First: 0, Count: 2, DTSStart: 0, Duration: 10},
		{Index: 1, TrackID: 1, First: 2, Count: 2, DTSStart: 10, Duration: 10},
		{Index: 2, TrackID: 2, First: 0, Count: 2, DTSStart: 0, Duration: 10},
		{Index: 3, TrackID: 2, First: 2, Count: 2, DTSStart: 10, Duration: 10},
	}
	for _, s := range segs {
		status := StatusOK
		var cause error
		if s.Index == 1 {
			status = StatusFailed
			cause = media.NewError(media.ClassInvalidSample, "worker", 0, "boom")
		}
		merger.Add(Result{Index: s.Index, TrackID: s.TrackID, Segment: s, Status: status, Err: cause, SHA256: "abc"})
	}
	man, err := merger.Build(Manifest{Tracks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := man.Invariants(map[uint32]uint32{1: 4, 2: 4}); err == nil {
		// count conservation is only over successful rows, so this should fail
		t.Fatal("invariants should report missing successful samples")
	}
	// Structural rules still pass when counts are not asserted (cancelled run).
	if err := man.Invariants(nil); err != nil {
		t.Fatalf("structural invariants failed: %v", err)
	}
	var failed *ManifestEntry
	for i := range man.Entries {
		if man.Entries[i].Index == 1 {
			failed = &man.Entries[i]
		}
	}
	if failed == nil || failed.SHA256 != "" {
		t.Fatalf("failed entry was not scrubbed: %+v", failed)
	}
	if failed.ErrClass != media.ClassInvalidSample {
		t.Errorf("error class = %s", failed.ErrClass)
	}
	if man.Entries[0].SHA256 == "" {
		t.Error("successful entry lost its evidence")
	}
	if err := man.VerifyManifest(); err != nil {
		t.Errorf("manifest self-verification failed: %v", err)
	}
}

func TestManifestDetectsTampering(t *testing.T) {
	man := &Manifest{Version: 1, Entries: []ManifestEntry{{Index: 0, Status: StatusOK, SHA256: "abc"}}}
	sha, err := man.Digest()
	if err != nil {
		t.Fatal(err)
	}
	man.SHA256 = sha
	if err := man.VerifyManifest(); err != nil {
		t.Fatal(err)
	}
	man.Entries[0].SHA256 = "tampered"
	if err := man.VerifyManifest(); err == nil {
		t.Fatal("tampered manifest passed verification")
	}
}
