package ingest

import (
	"errors"
	"testing"

	"regress-kernel/internal/media"
)

func TestValidateFramesClean(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	for _, rd := range fx.Renditions {
		rep, err := ValidateFrames(rd.Frames, rd.Config.Width, rd.Config.Height, DefaultFrameLimits())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rep.Failed() {
			t.Fatalf("clean rendition %s reported faults: %+v", rd.Config.Name, rep.Faults)
		}
		if rep.Accepted != len(rd.Frames) {
			t.Fatalf("accepted=%d want %d", rep.Accepted, len(rd.Frames))
		}
	}
}

func TestValidateFramesFaultCounters(t *testing.T) {
	cfg := media.DefaultConfig("corrupt", 17)
	cfg.Corrupt = media.Corruption{
		TruncateAt: 3, BadChecksumAt: 7, DuplicateAt: 11,
		MissingAt: 15, OutOfRangeAt: 19,
	}
	fx := media.Generate(cfg)
	rd := fx.Renditions[0]
	rep, err := ValidateFrames(rd.Frames, rd.Config.Width, rd.Config.Height, DefaultFrameLimits())
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !rep.Failed() {
		t.Fatal("corrupt fixture must fail validation")
	}
	want := map[string]int{
		"bad_hash":     2, // truncation and flipped byte both stale the hash
		"duplicate":    1,
		"missing":      3, // gap from the duplicate, the removed frame and the OOR frame
		"out_of_range": 1,
	}
	got := map[string]int{}
	for _, f := range rep.Faults {
		got[f.Kind]++
	}
	for kind, n := range want {
		if got[kind] != n {
			t.Fatalf("fault %s: got %d want %d (all: %v)", kind, got[kind], n, got)
		}
	}
	if rep.Counters.BadHash != 2 || rep.Counters.Duplicate != 1 ||
		rep.Counters.Missing != 3 || rep.Counters.OutOfRange != 1 {
		t.Fatalf("counter aggregate wrong: %+v", rep.Counters)
	}
	// Bad frame indexes must be recorded for the terminal report.
	for _, wantIdx := range []int{3, 7, 11, 15} {
		found := false
		for _, f := range rep.Faults {
			if f.Index == wantIdx {
				found = true
			}
		}
		if !found {
			t.Fatalf("no fault recorded at frame index %d: %+v", wantIdx, rep.Faults)
		}
	}
}

func TestValidateFramesMetadataAndTimestamp(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	frames := fx.Renditions[0].Frames
	wrong := append([]media.Frame(nil), frames...)
	wrong[4].Width = 7
	if rep, _ := ValidateFrames(wrong, frames[0].Width, frames[0].Height, DefaultFrameLimits()); !rep.Failed() {
		t.Fatal("metadata mismatch not detected")
	}
	nonMono := append([]media.Frame(nil), frames...)
	nonMono[4].TimestampTicks = nonMono[3].TimestampTicks
	rep, _ := ValidateFrames(nonMono, frames[0].Width, frames[0].Height, DefaultFrameLimits())
	found := false
	for _, f := range rep.Faults {
		if f.Kind == "timestamp" {
			found = true
		}
	}
	if !found {
		t.Fatal("non-monotonic timestamp not detected")
	}
}

func TestValidateFramesLimits(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	frames := fx.Renditions[0].Frames
	if _, err := ValidateFrames(frames, frames[0].Width, frames[0].Height,
		FrameLimits{MaxFrames: 10, MaxInputBytes: 1 << 30}); !errors.Is(err, media.ErrLimitExceeded) {
		t.Fatalf("frame cap: want ErrLimitExceeded, got %v", err)
	}
	if _, err := ValidateFrames(frames, frames[0].Width, frames[0].Height,
		FrameLimits{MaxFrames: 4096, MaxInputBytes: 1}); !errors.Is(err, media.ErrLimitExceeded) {
		t.Fatalf("byte cap: want ErrLimitExceeded, got %v", err)
	}
}
