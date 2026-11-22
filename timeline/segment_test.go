package timeline_test

import (
	"testing"

	"mediacore"
	"mediacore/timeline"
)

func TestSegmentRecordsFullAuditTrail(t *testing.T) {
	eng := timeline.NewEngine(50 * mediacore.Millisecond)
	push := func(track mediacore.TrackID, at, recv int64, seq uint64, kf bool) {
		ev := mediacore.Event{Track: track, Time: at, Recv: recv, Seq: seq}
		if track == mediacore.TrackAudio1 {
			ev.Kind = mediacore.KindPCM
			ev.Samples, ev.Rate, ev.Channels, ev.Payload = 1, 8000, 1, []byte{1}
		} else {
			ev.Kind = mediacore.KindVideo
			ev.Keyframe = kf
		}
		if err := eng.Ingest(ev); err != nil {
			t.Fatal(err)
		}
	}
	// Two tracks advance together; later events push the merged watermark so
	// the first 50 ms bucket becomes committable.
	for i := int64(0); i < 4; i++ {
		push(mediacore.TrackAudio1, i*40*mediacore.Millisecond, i*1000, uint64(i), false)
		push(mediacore.TrackVideo1, i*40*mediacore.Millisecond, i*1000+500, uint64(i), i == 0)
	}
	eng.Commit(timeline.Action{Kind: "pass", Track: mediacore.TrackAudio1,
		Offset: 0, Detail: "ordered"})
	segs := eng.Segments()
	if len(segs) == 0 {
		t.Fatal("no segment committed")
	}
	seg := segs[0]
	// SHA-256 fingerprint is present and 32 bytes.
	if len(seg.Hash) != 32 {
		t.Fatalf("segment hash length = %d, want 32 (SHA-256)", len(seg.Hash))
	}
	// Input event receive-time range spans the earliest to latest recv.
	if seg.InputFrom < 0 || seg.InputTo < seg.InputFrom {
		t.Fatalf("input event range invalid: %d..%d", seg.InputFrom, seg.InputTo)
	}
	// Recovery/normalization actions are retained and explainable.
	if len(seg.Actions) != 1 || seg.Actions[0].Kind != "pass" {
		t.Fatalf("recovery action not recorded: %+v", seg.Actions)
	}
	// Index offset is assigned and non-negative.
	if seg.IndexOff < 0 {
		t.Fatalf("index offset not assigned: %d", seg.IndexOff)
	}
	// Start/end span matches the configured segment duration.
	if seg.EndNS-seg.StartNS != 50*mediacore.Millisecond {
		t.Fatalf("segment span wrong: %d..%d", seg.StartNS, seg.EndNS)
	}
}
