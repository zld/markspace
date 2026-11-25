package ingest

import (
	"path/filepath"
	"testing"

	"mediavault/internal/fixture"
	"mediavault/internal/timeline"
)

func TestReadRTPWrapAndMonotonic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrap.rtp")
	spec := fixture.RTPSpec{
		SSRC: 7, ClockRate: 90000, PacketCount: 8,
		TimestampStep: 3000, PayloadSize: 20, Key: 3, Wrap: true,
	}
	if err := fixture.WriteRTP(path, spec); err != nil {
		t.Fatal(err)
	}
	refs, err := ReadRTPFile(path, RTPCapture{Source: "rtp", ClockRate: 90000, Kind: timeline.KindVideo})
	if err != nil {
		t.Fatal(err)
	}
	samples, errs, err := IngestRTP(refs, RTPCapture{Source: "rtp", ClockRate: 90000, Kind: timeline.KindVideo})
	if err != nil || len(errs) != 0 {
		t.Fatalf("wrap must be transparent: err=%v stageErrs=%v", err, errs)
	}
	if len(samples) != 8 {
		t.Fatalf("samples = %d, want 8", len(samples))
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].TS.DTS.Cmp(samples[i-1].TS.DTS) <= 0 {
			t.Fatalf("sample %d DTS not monotonic across wrap", i)
		}
	}
	if samples[len(samples)-1].TS.WrapCount == 0 {
		t.Fatal("wrap count must be recorded")
	}
}

func TestReadRTPBoundaryJumpReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jump.rtp")
	spec := fixture.RTPSpec{
		SSRC: 9, ClockRate: 90000, PacketCount: 6,
		TimestampStep: 3000, PayloadSize: 16, Key: 4,
		BoundaryJump: true, JumpIndex: 3,
	}
	if err := fixture.WriteRTP(path, spec); err != nil {
		t.Fatal(err)
	}
	capInfo := RTPCapture{Source: "rtp", ClockRate: 90000, Kind: timeline.KindVideo, JumpThreshold: 5000}
	refs, err := ReadRTPFile(path, capInfo)
	if err != nil {
		t.Fatal(err)
	}
	samples, errs, err := IngestRTP(refs, capInfo)
	if err != nil {
		t.Fatal(err)
	}
	var jump *timeline.StageError
	for _, se := range errs {
		if se.Kind == timeline.KindBoundaryJump {
			jump = se
		}
	}
	if jump == nil {
		t.Fatal("expected boundary-jump error")
	}
	if jump.Position < 4 || jump.Offset <= 0 {
		t.Fatalf("jump must carry the source-switch packet position and byte offset, got %+v", jump)
	}
	if !jump.Recoverable {
		t.Fatal("boundary jump must be marked recoverable")
	}
	// Samples before the jump remain usable; the jump packet is held out.
	if len(samples) != 3 {
		t.Fatalf("samples before jump = %d, want 3", len(samples))
	}
}

func TestReadRTPTruncatedCapture(t *testing.T) {
	dir := t.TempDir()
	full, err := fixture.BuildRTP(fixture.RTPSpec{
		SSRC: 1, ClockRate: 48000, PacketCount: 4,
		TimestampStep: 480, PayloadSize: 24, Key: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "trunc.rtp")
	if err := fixture.TruncateFile(path, full, len(full)-7); err != nil {
		t.Fatal(err)
	}
	_, err = ReadRTPFile(path, RTPCapture{Source: "rtp", ClockRate: 48000, Kind: timeline.KindAudio})
	se, ok := err.(*timeline.StageError)
	if !ok {
		t.Fatalf("want StageError, got %#v", err)
	}
	if se.Kind != timeline.KindTruncatedCapture || !se.Recoverable || se.Offset < 0 {
		t.Fatalf("want recoverable truncated-capture with offset, got %+v", se)
	}
}
