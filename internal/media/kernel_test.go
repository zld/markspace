package media_test

import (
	"testing"

	"media-archiver/internal/media"
)

func TestTimebaseFrameNanosecondRoundTrip(t *testing.T) {
	tb := media.Timebase{SampleRate: 48000}
	if got := tb.FramesToNS(240); got != 5_000_000 {
		t.Fatalf("FramesToNS(240)=%d, want 5,000,000", got)
	}
	if got := tb.NSToFrame(5_000_000); got != 240 {
		t.Fatalf("NSToFrame(5ms)=%d, want 240", got)
	}
	if bound := tb.AlignmentBoundNS(); bound != 20834 {
		t.Fatalf("alignment bound=%d, want 20834", bound)
	}
}

func TestTimebaseRejectsUnknownRate(t *testing.T) {
	if _, err := media.NewTimebase(12345); err == nil {
		t.Fatal("expected unsupported sample rate error")
	}
}

func TestPCMFormatValidation(t *testing.T) {
	good := media.PCMFormat{
		AudioFormat: 1, Channels: 2, SampleRate: 48000,
		ByteRate: 192000, BlockAlign: 4, BitsPerSample: 16,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid format rejected: %v", err)
	}

	bad := good
	bad.BlockAlign = 3
	err := bad.Validate()
	if err == nil {
		t.Fatal("expected invalid block alignment")
	}
	me := media.AsError(err)
	if me == nil || me.Code != media.CodeUnsupported {
		t.Fatalf("expected unsupported error, got %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*media.PCMFormat)
	}{
		{"channels", func(f *media.PCMFormat) { f.Channels = 9 }},
		{"bits", func(f *media.PCMFormat) { f.BitsPerSample = 12 }},
		{"rate", func(f *media.PCMFormat) { f.SampleRate = 12000 }},
		{"formattag", func(f *media.PCMFormat) { f.AudioFormat = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := good
			tc.mutate(&f)
			if err := f.Validate(); err == nil {
				t.Fatal("expected format rejection")
			}
		})
	}
}

func TestExitCodeMapping(t *testing.T) {
	if got := media.ExitCode(nil); got != 0 {
		t.Fatalf("nil exit=%d", got)
	}
	if got := media.ExitCode(media.NewError(media.CodeTruncated, media.StageParse, 42, "x", nil)); got != 2 {
		t.Fatalf("truncated exit=%d, want2", got)
	}
	if got := media.ExitCode(media.NewError(media.CodeInterrupted, media.StageIndex, -1, "x", nil)); got != 3 {
		t.Fatalf("interrupt exit=%d, want3", got)
	}
}
