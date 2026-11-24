package fingerprint

import (
	"testing"

	"mediad/internal/clock"
	"mediad/ts"
)

func ev(track string, idx, span int64, payload []byte) ts.TrackEvent {
	return ts.TrackEvent{TrackID: track, Start: idx * span, End: (idx + 1) * span, Payload: payload}
}

func TestFingerprintWindowing(t *testing.T) {
	// 2 ms windows, 1 ms per event => two windows from four events.
	f, err := NewFingerprinter(clock.FromMs(2))
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 4; i++ {
		f.Add(ev("a", i, clock.FromMs(1), []byte{byte(i + 1)}))
	}
	got := f.Flush()
	if len(got) != 2 {
		t.Fatalf("fingerprints = %d, want 2 windows", len(got))
	}
	if got[0].Start != 0 || got[0].End != clock.FromMs(2) {
		t.Fatalf("window 0 span = [%d,%d]", got[0].Start, got[0].End)
	}
	if got[1].WindowIndex != 1 || got[1].Start != clock.FromMs(2) {
		t.Fatalf("window 1 index/start = %d/%d", got[1].WindowIndex, got[1].Start)
	}
	for _, fp := range got {
		if len(fp.ID) != 64 || fp.ID != fp.ContentHash {
			t.Fatalf("fingerprint id must be 64-char hex hash, got %q", fp.ID)
		}
	}
}

func TestFingerprintSameContentReused(t *testing.T) {
	build := func() *Fingerprinter {
		f, _ := NewFingerprinter(clock.FromMs(2))
		f.Add(ev("a", 0, clock.FromMs(1), []byte{10, 20}))
		f.Add(ev("a", 1, clock.FromMs(1), []byte{30, 40}))
		return f
	}
	first := build()
	firstFps := first.Flush()
	if firstFps[0].Reused {
		t.Fatal("first compute must not be marked reused")
	}
	// A rerun preloads the prior store and recomputes identical content.
	second := build()
	second.Preload(firstFps)
	secondFps := second.Flush()
	if secondFps[0].ID != firstFps[0].ID {
		t.Fatalf("identical content must yield the same id: %q != %q", secondFps[0].ID, firstFps[0].ID)
	}
	if !secondFps[0].Reused {
		t.Fatal("rerun with same content must reuse the existing fingerprint")
	}
	if second.ReusedCount() != 1 {
		t.Fatalf("reused count = %d, want 1", second.ReusedCount())
	}
}

func TestFingerprintDifferentContentNotMerged(t *testing.T) {
	f, _ := NewFingerprinter(clock.FromMs(1))
	f.Add(ev("a", 0, clock.FromMs(1), []byte{1, 2, 3}))
	f.Add(ev("b", 0, clock.FromMs(1), []byte{4, 5, 6}))
	got := f.Flush()
	if len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("different content must produce distinct fingerprints: %+v", got)
	}
}

func TestFingerprintAdjacentPayloadNoCollision(t *testing.T) {
	// ["ab","c"] and ["a","bc"] concatenate to "abc" but must differ.
	f1, _ := NewFingerprinter(clock.FromMs(10))
	f1.Add(ev("a", 0, clock.FromMs(1), []byte("ab")))
	f1.Add(ev("a", 1, clock.FromMs(1), []byte("c")))
	f2, _ := NewFingerprinter(clock.FromMs(10))
	f2.Add(ev("a", 0, clock.FromMs(1), []byte("a")))
	f2.Add(ev("a", 1, clock.FromMs(1), []byte("bc")))
	h1 := f1.Flush()[0].ID
	h2 := f2.Flush()[0].ID
	if h1 == h2 {
		t.Fatal("length-prefixed hashing must distinguish adjacent payload splits")
	}
}

func TestFingerprintLookup(t *testing.T) {
	f, _ := NewFingerprinter(clock.FromMs(1))
	f.Add(ev("a", 0, clock.FromMs(1), []byte{9}))
	fps := f.Flush()
	if cached, ok := f.Lookup(fps[0].ContentHash); !ok || cached.ID != fps[0].ID {
		t.Fatal("cached fingerprint must be resolvable by content hash")
	}
	if _, ok := f.Lookup("deadbeef"); ok {
		t.Fatal("unknown hash must not resolve")
	}
}

func TestFingerprinterConfigValidation(t *testing.T) {
	if _, err := NewFingerprinter(0); err == nil {
		t.Fatal("zero window width must be rejected")
	}
	if _, err := NewFingerprinter(-5); err == nil {
		t.Fatal("negative window width must be rejected")
	}
}
