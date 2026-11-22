package timeline

import (
	"math/rand"
	"testing"
)

func mustEngine(t *testing.T, window Rat) *Engine {
	t.Helper()
	eng, err := NewEngine(window)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

func sampleEvents() []Event {
	evs := make([]Event, 0, 12)
	for i := 0; i < 10; i++ {
		evs = append(evs, Event{
			Epoch: 0,
			Time:  MustNewRat(int64(i+1)*100, 1000),
			Seq:   uint64(i + 1),
			ID:    "ev-" + string(rune('a'+i)),
			Kind:  "segment",
			Data:  []byte{byte(i)},
		})
	}
	// Two late events that must be inserted mid-timeline on replay.
	evs = append(evs,
		Event{Epoch: 0, Time: MustNewRat(250, 1000), Seq: 11, ID: "ev-late-1", Kind: "segment", Data: []byte{0xA1}},
		Event{Epoch: 0, Time: MustNewRat(550, 1000), Seq: 12, ID: "ev-late-2", Kind: "segment", Data: []byte{0xA2}},
	)
	return evs
}

func TestReplayDeterminism(t *testing.T) {
	window := MustNewRat(2000, 1000)
	evs := sampleEvents()

	// Reference: fully ordered ingestion.
	ref := mustEngine(t, window)
	ordered := append([]Event(nil), evs...)
	sortEvents(ordered)
	for _, ev := range ordered {
		if err := ref.Push(ev); err != nil {
			t.Fatalf("ordered push: %v", err)
		}
	}

	// Shuffled arrival, repeated with three seeds: digest must match.
	baseDigest := ref.Digest()
	var sawReplay bool
	for _, seed := range []int64{1, 42, 99} {
		rng := rand.New(rand.NewSource(seed))
		eng := mustEngine(t, window)
		shuffled := append([]Event(nil), evs...)
		rng.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		for _, ev := range shuffled {
			if err := eng.Push(ev); err != nil {
				t.Fatalf("seed %d push: %v", seed, err)
			}
		}
		if got := eng.Digest(); got != baseDigest {
			t.Fatalf("seed %d digest %s != reference %s", seed, got, baseDigest)
		}
		if got := len(eng.Committed()); got != len(evs) {
			t.Fatalf("seed %d committed %d events, want %d", seed, got, len(evs))
		}
		if eng.Metrics().Replayed > 0 {
			sawReplay = true
		}
	}
	if !sawReplay {
		t.Fatal("shuffled ingestion never triggered a bounded replay")
	}
}

func TestReplayWindowExceeded(t *testing.T) {
	eng := mustEngine(t, MustNewRat(1000, 1000))
	first := Event{Epoch: 0, Time: MustNewRat(10000, 1000), Seq: 1, ID: "head", Kind: "segment"}
	if err := eng.Push(first); err != nil {
		t.Fatalf("head push: %v", err)
	}
	tooLate := Event{Epoch: 0, Time: MustNewRat(8000, 1000), Seq: 2, ID: "late", Kind: "segment"}
	err := eng.Push(tooLate)
	if CodeOf(err) != "REPLAY_WINDOW_EXCEEDED" {
		t.Fatalf("want REPLAY_WINDOW_EXCEEDED, got %v", err)
	}
	if eng.Metrics().Rejected != 1 {
		t.Fatalf("rejected count = %d, want 1", eng.Metrics().Rejected)
	}
}

func TestSnapshotRecovery(t *testing.T) {
	window := MustNewRat(5000, 1000)
	eng := mustEngine(t, window)
	for i := 0; i < 5; i++ {
		if err := eng.Push(Event{
			Epoch: 0, Time: MustNewRat(int64(i+1)*100, 1000),
			Seq: uint64(i + 1), ID: id(i), Kind: "segment",
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	snap := eng.Snapshot()
	goodDigest := eng.Digest()

	// Corrupting burst: recovery restarts from the snapshot.
	late := []Event{
		{Epoch: 0, Time: MustNewRat(250, 1000), Seq: 6, ID: "late-a", Kind: "segment"},
		{Epoch: 0, Time: MustNewRat(450, 1000), Seq: 7, ID: "late-b", Kind: "segment"},
	}
	recovered, d1, err := RecoverFrom(snap, window, late)
	if err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	_, d2, err := RecoverFrom(snap, window, late)
	if err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("non-deterministic recovery: %s vs %s", d1, d2)
	}
	if d1 == goodDigest {
		t.Fatalf("digest should change after incorporating late events")
	}
	if len(recovered.Committed) != 7 {
		t.Fatalf("recovered committed %d, want 7", len(recovered.Committed))
	}
}

func TestDuplicateEventRejected(t *testing.T) {
	eng := mustEngine(t, MustNewRat(5000, 1000))
	ev := Event{Epoch: 0, Time: MustNewRat(100, 1000), Seq: 1, ID: "dup", Kind: "segment"}
	if err := eng.Push(ev); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := eng.Push(ev); CodeOf(err) != "DUPLICATE_EVENT" {
		t.Fatalf("want DUPLICATE_EVENT, got %v", err)
	}
}

func id(i int) string {
	return "ev-" + string(rune('a'+i))
}
