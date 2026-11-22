package timeline_test

import (
	"testing"

	"mediacore"
	"mediacore/timeline"
	"mediacore/audit"
)

func TestClockRTPWrapAndMapping(t *testing.T) {
	reg := timeline.NewClockRegistry()
	reg.Register(mediacore.TrackAudio1, 48000, 300)
	// Anchor the clock at extended timestamp 0 -> master 1s.
	if err := reg.Anchor(mediacore.TrackAudio1, 0, mediacore.Second); err != nil {
		t.Fatal(err)
	}
	// A raw timestamp near the 32-bit boundary, then one that wraps.
	near := uint32(0xFFFFFFF0)
	m1, orig1, ok, err := reg.Map(mediacore.TrackAudio1, near, 0)
	if err != nil || !ok {
		t.Fatalf("map near-wrap: %v", err)
	}
	// 16 ticks later in RTP terms -> wraps to small raw value.
	m2, orig2, _, err := reg.Map(mediacore.TrackAudio1, 0x00000000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !(orig2 > orig1) || m2 <= m1 {
		t.Fatalf("wrap extension failed: orig1=%d orig2=%d m1=%d m2=%d", orig1, orig2, m1, m2)
	}
	// Determinism: identical inputs map identically.
	m2b, _, _, _ := reg.Map(mediacore.TrackAudio1, 0x00000000, 0)
	if m2 != m2b {
		t.Fatal("clock mapping not deterministic")
	}
	// Unregistered track is a classified layout error.
	if _, _, _, err := reg.Map(mediacore.TrackVideo1, 1, 0); err == nil {
		t.Fatal("unregistered track accepted")
	}
}

func TestWatermarkOnlyAdvancesOnObservedEvents(t *testing.T) {
	wm := timeline.NewWatermark()
	wm.Observe(mediacore.Event{Track: mediacore.TrackAudio1, Time: 100})
	wm.Observe(mediacore.Event{Track: mediacore.TrackVideo1, Time: 50})
	if got := wm.Merged(); got != 50 {
		t.Fatalf("merged watermark = %d, want min(50,100)=50", got)
	}
	// Out-of-order older event never lowers a per-track mark.
	wm.Observe(mediacore.Event{Track: mediacore.TrackVideo1, Time: 10})
	if got, _ := wm.Track(mediacore.TrackVideo1); got != 50 {
		t.Fatalf("per-track watermark regressed to %d", got)
	}
	wm.Observe(mediacore.Event{Track: mediacore.TrackVideo1, Time: 200})
	if got := wm.Merged(); got != 100 {
		t.Fatalf("merged watermark = %d, want 100", got)
	}
}

func TestEngineCommitReplayAndHashInvariance(t *testing.T) {
	eng := timeline.NewEngine(100 * mediacore.Millisecond)
	push := func(track mediacore.TrackID, timeNS, recvNS int64, seq uint64) {
		if err := eng.Ingest(mediacore.Event{Track: track, Kind: mediacore.KindPCM,
			Time: timeNS, Recv: recvNS, Seq: seq, Samples: 1, Rate: 8000, Channels: 1}); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	// Two tracks, two complete segments worth of events.
	for i := int64(0); i < 4; i++ {
		push(mediacore.TrackAudio1, i*50*mediacore.Millisecond, i*10*mediacore.Millisecond, uint64(i))
		push(mediacore.TrackVideo1, i*50*mediacore.Millisecond, i*10*mediacore.Millisecond, uint64(i))
	}
	first := eng.Commit()
	if len(first) != 2 {
		t.Fatalf("want 2 committed segments, got %d", len(first))
	}
	before := audit.HashAll(eng.Segments())

	// Snapshot the unaffected segment hash before the late arrival.
	untouched := first[0].Hash

	// A late audio event inside the first segment bucket triggers replay.
	if err := eng.Ingest(mediacore.Event{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM,
		Time: 25 * mediacore.Millisecond, Recv: 900 * mediacore.Millisecond, Seq: 99}); err != nil {
		t.Fatalf("late ingest: %v", err)
	}
	if eng.Replays != 1 || eng.LateReplays != 1 || eng.Recomputed < 1 {
		t.Fatalf("replay counters wrong: replays=%d late=%d recomputed=%d",
			eng.Replays, eng.LateReplays, eng.Recomputed)
	}
	// Replay must rebuild from the affected bucket onward; bucket 0 was
	// affected, later buckets get identical content hashes.
	after := audit.HashAll(eng.Segments())
	if len(after) != len(before) {
		t.Fatalf("segment count changed: %d vs %d", len(after), len(before))
	}
	// The first segment changed (new event folded in); later ones did not.
	if equal(eng.Segments()[0].Hash, untouched) {
		t.Fatal("affected segment hash did not change after replay")
	}
	// VerifyReplay accounting: every surviving later segment matched.
	compared, matched, changed := audit.VerifyReplay(first, eng.Segments())
	if compared != 2 || matched != 1 || changed != 1 {
		t.Fatalf("replay verification: compared=%d matched=%d changed=%d", compared, matched, changed)
	}
}

func TestRepeatedRunHashesIdentical(t *testing.T) {
	build := func() []string {
		eng := timeline.NewEngine(100 * mediacore.Millisecond)
		// Same events delivered in shuffled order on each run.
		evs := make([]mediacore.Event, 0)
		for i := int64(0); i < 6; i++ {
			evs = append(evs, mediacore.Event{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM,
				Time: i * 40 * mediacore.Millisecond, Recv: i * 5, Seq: uint64(i),
				Rate: 8000, Channels: 1})
		}
		order := []int{3, 0, 5, 1, 4, 2}
		for _, i := range order {
			_ = eng.Ingest(evs[i])
		}
		eng.Commit()
		return audit.HashAll(eng.Segments())
	}
	run1, run2 := build(), build()
	if !audit.EqualRuns(run1, run2) {
		t.Fatalf("repeated runs produced different hashes:\n%v\n%v", run1, run2)
	}
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
