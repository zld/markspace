package manifest

import (
	"testing"

	"mediarebuild/internal/model"
)

func entries() []Entry {
	return []Entry{
		{Key: "b-task", Kind: "rtp", Metrics: map[string]uint64{"packets": 10}},
		{Key: "a-task", Kind: "mp4", Hashes: map[string]string{"frame": "deadbeef"}},
		{Key: "c-task", Kind: "rtp", Diagnostics: model.Diags{
			model.NewDiag(model.StageRTP, "loss", 12, true, "missing 3 packets"),
		}},
	}
}

func TestDeterministicHash(t *testing.T) {
	m1 := NewMerger()
	for _, e := range entries() {
		m1.Add(e)
	}
	// Same entries, different insertion order.
	m2 := NewMerger()
	es := entries()
	for i := len(es) - 1; i >= 0; i-- {
		m2.Add(es[i])
	}
	man1, h1 := m1.Finalize()
	_, h2 := m2.Finalize()
	if h1 != h2 {
		t.Fatal("hash differs for identical entry sets")
	}
	if man1.Entries[0].Key != "a-task" || man1.Entries[2].Key != "c-task" {
		t.Fatalf("entries not sorted: %+v", man1.Entries)
	}
}

func TestRetryDoesNotDoubleCount(t *testing.T) {
	m := NewMerger()
	m.Add(Entry{Key: "t", Kind: "rtp", Metrics: map[string]uint64{"packets": 5}})
	// Simulate a retry of the same task with the same result.
	m.Add(Entry{Key: "t", Kind: "rtp", Metrics: map[string]uint64{"packets": 5}})
	man, _ := m.Finalize()
	if len(man.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(man.Entries))
	}
	if man.Entries[0].Metrics["packets"] != 5 {
		t.Fatalf("packets = %d, want 5 (no double count)", man.Entries[0].Metrics["packets"])
	}
}

func TestEmptyKeyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for empty key")
		}
	}()
	NewMerger().Add(Entry{Kind: "x"})
}
