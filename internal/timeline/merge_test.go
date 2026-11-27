package timeline

import (
	"testing"

	"medtrace/internal/mederr"
)

func TestClockConvert(t *testing.T) {
	c, err := NewClock("webm", 1_000_000) // Matroska default: 1ms
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Convert(42)
	if err != nil || got != Timestamp(42_000_000) {
		t.Fatalf("convert = %d, err %v", got, err)
	}
	if _, err := NewClock("bad", 0); mederr.AsError(err) == nil {
		t.Fatal("zero period must fail")
	}
}

func TestClockConvertOverflow(t *testing.T) {
	c, _ := NewClock("fast", 1_000_000_000)
	_, err := c.Convert(1 << 62)
	if mederr.AsError(err) == nil || mederr.AsError(err).Category != mederr.CatOverflow {
		t.Fatalf("want overflow error, got %v", err)
	}
	if _, err := Timestamp(1<<63 - 1).Add(1); mederr.AsError(err) == nil {
		t.Fatal("addition overflow must be reported")
	}
}

func TestMergeStableOrdering(t *testing.T) {
	// Same time across two tracks: track number breaks the tie; same track
	// uses block sequence; equal keys keep insertion order.
	in := []Event{
		{TimeNs: 100, TrackID: 2, BlockSeq: 0, Kind: "video"},
		{TimeNs: 100, TrackID: 1, BlockSeq: 9, Kind: "audio"},
		{TimeNs: 50, TrackID: 1, BlockSeq: 0, Kind: "audio"},
		{TimeNs: 100, TrackID: 2, BlockSeq: 0, Kind: "dup"},
	}
	out := MergeStable(in)
	want := []struct {
		ns    Timestamp
		track uint64
		seq   uint64
	}{
		{50, 1, 0}, {100, 1, 9}, {100, 2, 0}, {100, 2, 0},
	}
	if len(out) != len(want) {
		t.Fatalf("len = %d", len(out))
	}
	for i, w := range want {
		if out[i].TimeNs != w.ns || out[i].TrackID != w.track || out[i].BlockSeq != w.seq {
			t.Fatalf("pos %d = %+v, want %+v", i, out[i], w)
		}
	}
	if out[2].Kind != "video" || out[3].Kind != "dup" {
		t.Fatalf("equal-key stability lost: %q %q", out[2].Kind, out[3].Kind)
	}
	// Input slice must not be mutated.
	if in[0].Kind != "video" {
		t.Fatal("MergeStable must not mutate its input")
	}
}

func TestMergeTracks(t *testing.T) {
	tracks := map[uint64][]Event{
		2: {{TimeNs: 20, TrackID: 2, BlockSeq: 0}, {TimeNs: 60, TrackID: 2, BlockSeq: 1}},
		1: {{TimeNs: 10, TrackID: 1, BlockSeq: 0}, {TimeNs: 60, TrackID: 1, BlockSeq: 1}},
	}
	out := MergeTracks(tracks)
	wantKeys := []struct {
		ns    Timestamp
		track uint64
	}{
		{10, 1}, {20, 2}, {60, 1}, {60, 2},
	}
	if len(out) != len(wantKeys) {
		t.Fatalf("merge len = %d, want %d", len(out), len(wantKeys))
	}
	for i, w := range wantKeys {
		if out[i].TimeNs != w.ns || out[i].TrackID != w.track {
			t.Fatalf("merge order wrong at %d: %+v, want %+v", i, out[i], w)
		}
	}
}

func TestStreamMerger(t *testing.T) {
	mk := func(evs []Event) func() (Event, bool, error) {
		i := 0
		return func() (Event, bool, error) {
			if i >= len(evs) {
				return Event{}, false, nil
			}
			e := evs[i]
			i++
			return e, true, nil
		}
	}
	m := NewStreamMerger(
		mk([]Event{{TimeNs: 5, TrackID: 1, BlockSeq: 0}, {TimeNs: 9, TrackID: 1, BlockSeq: 1}}),
		mk([]Event{{TimeNs: 6, TrackID: 2, BlockSeq: 0}}),
	)
	var out []Event
	if err := m.Drain(func(e Event) error { out = append(out, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].TimeNs != 5 || out[1].TimeNs != 6 || out[2].TimeNs != 9 {
		t.Fatalf("k-way merge order wrong: %+v", out)
	}
}

func TestAbsDiff(t *testing.T) {
	if d := AbsDiff(100, 40); d != 60 {
		t.Fatalf("abs diff = %d", d)
	}
}
