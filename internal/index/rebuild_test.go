package index

import (
	"encoding/binary"
	"testing"

	"mediad/internal/model"
)

func vid(seq uint64, key bool) *model.Event {
	return &model.Event{Kind: model.KindPacket, Media: model.MediaVideo, Seq: seq, Keyframe: key}
}

func TestRebuildCountsGOPs(t *testing.T) {
	// Two complete GOPs, each beginning with a keyframe.
	var samples []*model.Event
	seq := uint64(100)
	for i := 0; i < 4; i++ {
		samples = append(samples, vid(seq, true))
		seq++
		for j := 0; j < 2; j++ {
			samples = append(samples, vid(seq, false))
			seq++
		}
	}
	res := Rebuild(samples)
	if len(res.Keyframes) != 4 {
		t.Fatalf("keyframes = %v, want 4", res.Keyframes)
	}
	if res.Recoverable != 4 || res.Unrecoverable != 0 {
		t.Fatalf("recoverable=%d unrecoverable=%d", res.Recoverable, res.Unrecoverable)
	}
	if res.StssSize == 0 || len(res.Stss) != res.StssSize {
		t.Fatalf("stss not marshaled: size=%d len=%d", res.StssSize, len(res.Stss))
	}
	// go-mp4 marshals the box payload: 4-byte version/flags, 4-byte entry
	// count, then one 32-bit sample number per keyframe.
	if want := 4 + 4 + 4*4; res.StssSize != want {
		t.Fatalf("stss size = %d, want %d", res.StssSize, want)
	}
	if got := binary.BigEndian.Uint32(res.Stss[4:8]); got != 4 {
		t.Fatalf("stss entry count = %d, want 4", got)
	}
}

func TestRebuildMarksHoledGOPUnrecoverable(t *testing.T) {
	samples := []*model.Event{
		vid(1, true), vid(2, false),
		// Gap: seq 3 is missing, so GOP 2 starts at a discontinuity.
		vid(4, true), vid(5, false),
	}
	res := Rebuild(samples)
	if res.Recoverable != 1 {
		t.Fatalf("recoverable = %d, want 1 (second GOP is holed)", res.Recoverable)
	}
	if res.Unrecoverable != 1 {
		t.Fatalf("unrecoverable = %d, want 1", res.Unrecoverable)
	}
	if len(res.Keyframes) != 1 || res.Keyframes[0] != 1 {
		t.Fatalf("keyframes = %v, want [1]", res.Keyframes)
	}
}

func TestRebuildGOPWithoutKeyframeUnrecoverable(t *testing.T) {
	samples := []*model.Event{vid(1, false), vid(2, false), vid(3, false)}
	res := Rebuild(samples)
	if res.Recoverable != 0 || res.Unrecoverable != 1 {
		t.Fatalf("recoverable=%d unrecoverable=%d", res.Recoverable, res.Unrecoverable)
	}
	if len(res.Keyframes) != 0 {
		t.Fatalf("keyframes = %v, want empty", res.Keyframes)
	}
}

func TestRebuildDeterministic(t *testing.T) {
	samples := []*model.Event{vid(1, true), vid(2, false), vid(3, true)}
	a := Rebuild(samples)
	b := Rebuild(samples)
	if a.StssSHA256 != b.StssSHA256 {
		t.Fatal("stss digest is not deterministic")
	}
}
