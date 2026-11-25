package manifest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func sampleClip(key, event string) Clip {
	return Clip{
		TaskKey: key, Event: event, Path: "clips/" + event + ".mp4",
		Bytes: 1024, AudioFrames: 48000, AudioPeak: "0.500000",
		VideoFirst: 0, VideoLast: 29, LeadInMs: 33,
		FrameHash: "abc", PixelMean: "0.250000",
	}
}

func TestDeterministicAcrossOrder(t *testing.T) {
	clips := []Clip{
		sampleClip("task/01", "a"),
		sampleClip("task/02", "b"),
		sampleClip("task/03", "c"),
	}
	m1 := New("task", "inhash")
	for _, c := range clips {
		if err := m1.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	doc1, err := m1.Seal()
	if err != nil {
		t.Fatal(err)
	}
	out1, err := Encode(doc1)
	if err != nil {
		t.Fatal(err)
	}

	m2 := New("task", "inhash")
	for i := len(clips) - 1; i >= 0; i-- {
		if err := m2.Add(clips[i]); err != nil {
			t.Fatal(err)
		}
	}
	doc2, err := m2.Seal()
	if err != nil {
		t.Fatal(err)
	}
	out2, err := Encode(doc2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out1, out2) {
		t.Fatal("manifest bytes differ between insertion orders")
	}
	if doc1.Hash != doc2.Hash {
		t.Fatalf("hashes differ: %s vs %s", doc1.Hash, doc2.Hash)
	}
}

func TestConcurrentMergeNoDuplicates(t *testing.T) {
	m := New("task", "in")
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each key is offered twice, identically: retries must be
			// idempotent, not rejected.
			key := "k" + string(rune('A'+i%25))
			c := sampleClip(key, "e")
			if err := m.Add(c); err != nil {
				errs <- err
			}
			if err := m.Add(c); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	doc, err := m.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Clips) != 25 {
		t.Fatalf("clips = %d, want 25", len(doc.Clips))
	}
	seen := map[string]bool{}
	for _, c := range doc.Clips {
		if seen[c.TaskKey] {
			t.Fatalf("duplicate key %s in output", c.TaskKey)
		}
		seen[c.TaskKey] = true
	}
}

func TestConflictingDuplicateRejected(t *testing.T) {
	m := New("task", "in")
	if err := m.Add(sampleClip("k1", "a")); err != nil {
		t.Fatal(err)
	}
	c := sampleClip("k1", "b")
	if err := m.Add(c); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestEmptyKeyRejected(t *testing.T) {
	m := New("task", "in")
	if err := m.Add(Clip{Event: "a"}); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestSealClosedToAdds(t *testing.T) {
	m := New("task", "in")
	if _, err := m.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(sampleClip("k", "e")); err == nil {
		t.Fatal("add after seal should fail")
	}
}

func TestWriteAtomicAndVerify(t *testing.T) {
	dir := t.TempDir()
	m := New("task", "in")
	for _, k := range []string{"k3", "k1", "k2"} {
		if err := m.Add(sampleClip(k, "e")); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := m.Seal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := WriteAtomic(path, doc); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("manifest should end with newline")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
	got, err := HashOf(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got != doc.Hash {
		t.Fatalf("recomputed hash %s != %s", got, doc.Hash)
	}
}

func TestEncodeRejectsTamperedHash(t *testing.T) {
	m := New("task", "in")
	if err := m.Add(sampleClip("k", "e")); err != nil {
		t.Fatal(err)
	}
	doc, err := m.Seal()
	if err != nil {
		t.Fatal(err)
	}
	doc.Hash = "deadbeef"
	if _, err := Encode(doc); err == nil {
		t.Fatal("tampered hash should be rejected")
	}
}
