package manifest

import (
	"sync"
	"testing"
)

func TestStableDigestAndSorting(t *testing.T) {
	items := []*Item{
		{StableKey: "TASK-z", SampleID: "z", InputPath: "/z"},
		{StableKey: "TASK-a", SampleID: "a", InputPath: "/a"},
	}
	m1 := BuildManifest(items, nil)
	m2 := BuildManifest([]*Item{items[1], items[0]}, nil)
	if m1.Digest != m2.Digest {
		t.Fatalf("digests differ: %s != %s", m1.Digest, m2.Digest)
	}
	if m1.Items[0].StableKey != "TASK-a" {
		t.Fatalf("sort order wrong: %s", m1.Items[0].SampleID)
	}
	if m1.Items[0].OutputItemID == "" || m1.Items[0].OutputItemID != OutputID("TASK-a") {
		t.Fatal("output item ID not assigned deterministically")
	}
}

func TestConcurrentDuplicateSubmissionOneOutput(t *testing.T) {
	mr := NewMerger()
	key := TaskKey("duplicate")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, dup := mr.Submit(&Item{StableKey: key, SampleID: "dup", InputPath: "/same"})
			if i == 0 {
				_ = dup
			}
		}(i)
	}
	wg.Wait()
	items := mr.Items()
	if len(items) != 1 {
		t.Fatalf("expected one output item, got %d", len(items))
	}
	if items[0].SubmissionCount != 100 {
		t.Fatalf("submission count = %d", items[0].SubmissionCount)
	}
}

func TestSameInputRepeatDigest(t *testing.T) {
	build := func() string {
		m := BuildManifest([]*Item{{StableKey: "k", SHA256: "abc"}}, []SampleMapping{{SampleID: "s", StableKey: "k"}})
		return m.Digest
	}
	if build() != build() {
		t.Fatal("repeated manifest digest changed")
	}
}
