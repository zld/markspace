package media

import (
	"errors"
	"testing"
)

func TestConvertRoundTrip(t *testing.T) {
	video := Must(VideoScale)
	audio := Must(48000)
	// 1 second of video ticks == 48000 audio ticks.
	if got := video.Convert(90000, 48000); got != 48000 {
		t.Fatalf("Convert(90000 -> 48000) = %d", got)
	}
	if got := audio.Convert(48000, VideoScale); got != 90000 {
		t.Fatalf("Convert(48000 -> 90000) = %d", got)
	}
	if ms := video.ToMillis(90000); ms != 1000 {
		t.Fatalf("ToMillis = %d", ms)
	}
	if tk := video.FromMillis(33); tk != 2970 {
		t.Fatalf("FromMillis = %d", tk)
	}
}

func TestZeroScaleRejected(t *testing.T) {
	if _, err := New(0); err == nil {
		t.Fatal("expected error for zero scale")
	}
}

func TestUnwrapTimestampWrap(t *testing.T) {
	var u Unwrapper
	base := uint32(0xFFFFFFF0)
	for i, raw := range []uint32{base, base + 5, 4, 9} { // wraps at 2^32
		got := u.Unwrap(raw)
		want := int64(base) + int64([]uint32{0, 5, 20, 25}[i])
		if got != want {
			t.Fatalf("step %d: Unwrap(%#x) = %d, want %d", i, raw, got, want)
		}
	}
}

func TestUnwrapLatePacketFromPreviousCycle(t *testing.T) {
	var u Unwrapper
	u.Unwrap(0xFFFFFFFE)
	u.Unwrap(10) // wrapped forward
	late := u.Unwrap(0xFFFFFFFC)
	if late != 0xFFFFFFFC {
		t.Fatalf("late packet resolved to %d, want %d", late, 0xFFFFFFFC)
	}
}

func TestSeqUnwrap(t *testing.T) {
	var s SeqUnwrapper
	s.Unwrap(65534)
	if got := s.Unwrap(65535); got != 65535 {
		t.Fatalf("got %d", got)
	}
	if got := s.Unwrap(0); got != 65536 {
		t.Fatalf("wrap: got %d, want 65536", got)
	}
	if got := s.Unwrap(65533); got != 65533 { // reordered old packet
		t.Fatalf("reorder: got %d, want 65533", got)
	}
}

func TestErrorClassification(t *testing.T) {
	err := Fault(CodeIndexInvalid, "ingest.ValidateIndex", 128, "offset out of bounds")
	if !errors.Is(err, &Error{Code: CodeIndexInvalid}) {
		t.Fatal("errors.Is by code failed")
	}
	if CodeOf(err) != CodeIndexInvalid {
		t.Fatal("CodeOf mismatch")
	}
	if ClassOf(CodeOf(err)) != ClassInput {
		t.Fatal("index_invalid must be input class")
	}
	if ClassOf(CodeInternal) != ClassInternal {
		t.Fatal("internal must be internal class")
	}
	wrapped := Wrap(CodeNALCorrupt, "ingest.Scan", 7, err, "bad start code")
	if !errors.Is(wrapped, err) {
		t.Fatal("wrapped cause not visible")
	}
}
