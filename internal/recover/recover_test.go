package recover

import (
	"errors"
	"testing"

	"rtptrace/internal/manifest"
	"rtptrace/internal/timeline"
)

func TestCorruptionItemLocationAndPrefixRecovery(t *testing.T) {
	err := &manifest.CorruptionError{Code: "truncated-stbl", TrackID: 1, Sample: 38}
	plain := CorruptionItem(err, false)
	if plain.TrackID != 1 || plain.FailureSample != 38 || plain.RecoveryState != StateRecoveryRequired {
		t.Fatalf("plain=%+v", plain)
	}
	recovered := CorruptionItem(err, true)
	if recovered.RecoveryState != StateRecoveredPrefix {
		t.Fatalf("recovered=%+v", recovered)
	}
	if !errors.As(err, new(*manifest.CorruptionError)) {
		t.Fatal("typed error assertion failed")
	}
}

func TestRecoverEditListClampsOverflow(t *testing.T) {
	l := timeline.EditList{MovieTimescale: 1000, MediaTimescale: 24000, Edits: []timeline.Edit{
		{SegmentDuration: 3000, MediaTime: 200000},
	}}
	// Media duration 240000 leaves 40000 media ticks after media_time.
	rep := RecoverEditList(1, l, 240000, true)
	if rep.State != StateRecoveredEditClamped || len(rep.Adjustments) != 1 {
		t.Fatalf("report=%+v", rep)
	}
	got := rep.Edits.Edits[0].SegmentDuration
	if got != 1666 { // floor(40000*1000/24000)
		t.Fatalf("clamped duration=%d", got)
	}
	if rep.Adjustments[0].TimeErrorTicks <= 0 {
		t.Fatalf("time deviation=%+v", rep.Adjustments[0])
	}

	disabled := RecoverEditList(1, l, 240000, false)
	if disabled.State != StateRecoveryRequired || disabled.Edits.Edits[0].SegmentDuration != 3000 {
		t.Fatalf("disabled recovery modified edit: %+v", disabled)
	}
}
