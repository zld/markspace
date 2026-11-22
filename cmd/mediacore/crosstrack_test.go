package main

import (
	"testing"

	"mediacore"
	"mediacore/audit"
	"mediacore/timeline"
)

// TestCrossTrackLateReplay verifies that a late audio event arriving after a
// video track has advanced the shared timeline triggers a deterministic
// replay that rebuilds the affected bucket while leaving others intact.
func TestCrossTrackLateReplay(t *testing.T) {
	eng := timeline.NewEngine(50 * mediacore.Millisecond)
	push := func(track mediacore.TrackID, at, recv int64, seq uint64) {
		if err := eng.Ingest(mediacore.Event{Track: track, Kind: mediacore.KindPCM,
			Time: at, Recv: recv, Seq: seq, Rate: 8000, Channels: 1, Samples: 1}); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	// Six events per track at 40 ms spacing -> times up to 200 ms so several
	// 50 ms buckets sit fully below the merged watermark after Commit.
	for i := int64(0); i < 6; i++ {
		push(mediacore.TrackAudio1, i*40*mediacore.Millisecond, i*10, uint64(i))
		push(mediacore.TrackVideo1, i*40*mediacore.Millisecond, i*10, uint64(i))
	}
	eng.Commit()
	before := audit.HashAll(eng.Segments())
	if len(before) < 2 {
		t.Fatalf("need multiple committed buckets, got %d", len(before))
	}
	// Late audio in the earliest bucket, long after video moved the watermark.
	if err := eng.Ingest(mediacore.Event{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM,
		Time: 20 * mediacore.Millisecond, Recv: 5 * mediacore.Second, Seq: 99,
		Rate: 8000, Channels: 1, Samples: 1}); err != nil {
		t.Fatalf("late ingest: %v", err)
	}
	eng.Commit()
	if eng.LateReplays < 1 {
		t.Fatal("cross-track late event did not trigger replay")
	}
	after := audit.HashAll(eng.Segments())
	if len(after) != len(before) {
		t.Fatalf("bucket count changed: %d vs %d", len(after), len(before))
	}
	// Earliest bucket changed; a later, unaffected bucket kept its hash.
	if after[len(after)-1] != before[len(before)-1] {
		t.Fatal("unaffected later bucket hash changed on replay")
	}
	if after[0] == before[0] {
		t.Fatal("affected bucket hash did not change after late replay")
	}
}
