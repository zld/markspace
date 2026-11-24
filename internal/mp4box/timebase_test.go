package mp4box

import (
	"bytes"
	"testing"

	"mediapipe/internal/clock"
	"mediapipe/internal/model"
)

func TestWriteAndParseMdhd(t *testing.T) {
	tb, err := RoundTrip(48000, 96000) // 2 seconds at 48 kHz
	if err != nil {
		t.Fatal(err)
	}
	if tb.TrackTimescale != 48000 || tb.TrackDuration != 96000 {
		t.Fatalf("round trip = %+v", tb)
	}
}

func TestTimeBaseConversions(t *testing.T) {
	// 48000 container units for one second -> 90000 shared ticks.
	ticks, err := To90k(48000, 48000)
	if err != nil || ticks != clock.Ticks(90000) {
		t.Fatalf("To90k = %d, err=%v", ticks, err)
	}
	units, err := From90k(clock.Ticks(90000), 44100)
	if err != nil || units != 44100 {
		t.Fatalf("From90k = %d, err=%v", units, err)
	}
	if _, err := To90k(1, 0); model.AsMediaError(err) == nil {
		t.Fatal("zero timescale must be rejected")
	}
}

func TestParseRejectsEmptyStream(t *testing.T) {
	_, err := Parse(bytes.NewReader(nil))
	// An empty stream yields an IO-classified error rather than a panic.
	if model.AsMediaError(err) == nil {
		t.Fatal("empty mp4 stream must be classified, not silently accepted")
	}
}
