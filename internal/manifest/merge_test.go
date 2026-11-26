package manifest_test

import (
	"strings"
	"testing"

	"mediad/internal/manifest"
)

func sampleEntries() []manifest.Entry {
	return []manifest.Entry{
		{TaskKey: "t2", TrackKey: "video", SampleIdx: 1, ObjectHash: "aa", Size: 10, StartNs: 0, DurationNs: 5},
		{TaskKey: "t1", TrackKey: "audio", SampleIdx: 2, ObjectHash: "bb", Size: 20, StartNs: 5, DurationNs: 5},
		{TaskKey: "t1", TrackKey: "audio", SampleIdx: 1, ObjectHash: "cc", Size: 30, StartNs: 0, DurationNs: 5},
	}
}

func TestMergeSortsByTaskTrackSample(t *testing.T) {
	got, err := manifest.Merge(sampleEntries())
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		task, track string
		sample      int
	}{
		{"t1", "audio", 1}, {"t1", "audio", 2}, {"t2", "video", 1},
	}
	for i, e := range got {
		if e.TaskKey != want[i].task || e.TrackKey != want[i].track ||
			e.SampleIdx != want[i].sample {
			t.Fatalf("position %d = %s/%s/%d, want %+v",
				i, e.TaskKey, e.TrackKey, e.SampleIdx, want[i])
		}
	}
}

func TestMergeDedupIsIdempotent(t *testing.T) {
	first, err := manifest.Merge(sampleEntries())
	if err != nil {
		t.Fatal(err)
	}
	d1 := manifest.Digest(first)

	// Re-merge the same entries plus an identical duplicate batch.
	again, err := manifest.Merge(first, sampleEntries())
	if err != nil {
		t.Fatal(err)
	}
	d2 := manifest.Digest(again)
	if len(again) != len(first) {
		t.Fatalf("duplicate run changed entry count: %d -> %d", len(first), len(again))
	}
	if d1 != d2 {
		t.Fatalf("digest changed on duplicate run: %s vs %s", d1, d2)
	}
	if len(d1) != 64 || strings.ToLower(d1) != d1 {
		t.Fatalf("digest not 64-char lowercase hex: %q", d1)
	}
}

func TestMergeRejectsConflictingDuplicate(t *testing.T) {
	base := sampleEntries()
	conflict := manifest.Entry{
		TaskKey: "t1", TrackKey: "audio", SampleIdx: 1,
		ObjectHash: "DIFFERENT", Size: 99, StartNs: 0, DurationNs: 5,
	}
	if _, err := manifest.Merge(base, []manifest.Entry{conflict}); err == nil {
		t.Fatal("conflicting duplicate must be rejected")
	}
}

func TestMergeRejectsBlankKeys(t *testing.T) {
	bad := []manifest.Entry{{TaskKey: "", TrackKey: "v", SampleIdx: 1}}
	if _, err := manifest.Merge(bad); err == nil {
		t.Fatal("blank task key must be rejected")
	}
}

func TestDigestStableAcrossInputOrder(t *testing.T) {
	a, _ := manifest.Merge(sampleEntries())
	reversed := []manifest.Entry{
		sampleEntries()[2], sampleEntries()[1], sampleEntries()[0],
	}
	b, _ := manifest.Merge(reversed)
	if manifest.Digest(a) != manifest.Digest(b) {
		t.Fatal("digest must not depend on input ordering")
	}
}

func TestSerializeRoundTripsShape(t *testing.T) {
	got, _ := manifest.Merge(sampleEntries())
	text := manifest.Serialize(got)
	if len(text) == 0 || !strings.Contains(string(text), "t1") {
		t.Fatalf("serialization malformed: %q", text)
	}
	if strings.Count(string(text), "\n") != len(got) {
		t.Fatal("one line per entry expected")
	}
}
