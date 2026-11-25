package merge

import (
	"testing"

	"mediacore/internal/model"
)

func results() []TaskResult {
	return []TaskResult{
		{TaskKey: "b-task", SampleID: "s2", Events: []model.Event{
			{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 3000, SrcOffset: 9, OutTrackID: "out:v", OutOffset: 500, Size: 3, Payload: []byte("xyz")},
		}},
		{TaskKey: "a-task", SampleID: "s1", Events: []model.Event{
			{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 0, SrcOffset: 1, OutTrackID: "out:v", OutOffset: 100, Size: 3, Payload: []byte("abc")},
			{TrackID: "mp4:2", Kind: model.EventSubtitle, Ticks: 0, SrcOffset: 2, OutTrackID: "out:s", OutOffset: 200, Size: 2, Payload: []byte("hi")},
		}},
	}
}

func TestBuildDeterministicAndDedup(t *testing.T) {
	m1 := Build(results())
	// Reordered input plus a duplicate task key must not change output.
	rs := results()
	rs[0], rs[1] = rs[1], rs[0]
	rs = append(rs, results()[1]) // duplicate a-task
	m2 := Build(rs)
	if m1.Digest != m2.Digest {
		t.Fatalf("digest not deterministic: %s vs %s", m1.Digest, m2.Digest)
	}
	if len(m2.Items) != 3 {
		t.Fatalf("items = %d, want 3 after dedup", len(m2.Items))
	}
	// Sorted by task key first.
	if m1.Items[0].TaskKey != "a-task" || m1.Items[2].TaskKey != "b-task" {
		t.Fatalf("bad order: %+v", m1.Items)
	}
	// SHA-256 of "abc" is well known; find the item carrying it.
	wantSHA := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	found := false
	for _, it := range m1.Items {
		if it.SHA256 == wantSHA {
			found = true
		}
	}
	if !found {
		t.Fatalf("no item with sha256 %s: %+v", wantSHA, m1.Items)
	}
	// Canonical bytes identical across builds.
	if string(m1.Marshal()) != string(m2.Marshal()) {
		t.Fatal("canonical bytes differ")
	}
}

func TestBuildEmpty(t *testing.T) {
	m := Build(nil)
	if m.Digest == "" || len(m.Items) != 0 {
		t.Fatalf("empty manifest: %+v", m)
	}
}
