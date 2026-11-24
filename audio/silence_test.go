package audio

import (
	"testing"

	"mediad/internal/clock"
	"mediad/ts"
)

func ev(track string, start, end int64, payload []byte) ts.TrackEvent {
	return ts.TrackEvent{TrackID: track, Start: start, End: end, Payload: payload}
}

func TestSilenceIntervalReported(t *testing.T) {
	// Three silent 90-tick packets (1 ms each), threshold 10, min 2 ms.
	d, err := NewDetector(DetectorConfig{Threshold: 10, MinDurationTicks: clock.FromMs(2)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		d.Observe(ev("a", int64(i)*90, int64(i+1)*90, []byte{5, 5, 5}), nil)
	}
	got := d.Flush()
	if len(got) != 1 {
		t.Fatalf("intervals = %+v, want one", got)
	}
	if got[0].Start != 0 || got[0].End != 270 {
		t.Fatalf("interval = [%d,%d], want [0,270]", got[0].Start, got[0].End)
	}
	// Rounded milliseconds stay within 1 ms of the tick boundary.
	if got[0].StartMs() != 0 || got[0].EndMs() != 3 {
		t.Fatalf("ms = [%d,%d], want [0,3]", got[0].StartMs(), got[0].EndMs())
	}
}

func TestLoudPayloadBreaksInterval(t *testing.T) {
	// Minimum two packets so the single-packet window before the loud
	// sample is suppressed, leaving only the later sustained silence.
	d, _ := NewDetector(DetectorConfig{Threshold: 10, MinDurationTicks: 180})
	d.Observe(ev("a", 0, 90, []byte{2}), nil)
	d.Observe(ev("a", 90, 180, []byte{200}), nil) // loud: closes window
	d.Observe(ev("a", 180, 270, []byte{2}), nil)
	d.Observe(ev("a", 270, 360, []byte{2}), nil)
	got := d.Flush()
	if len(got) != 1 || got[0].Start != 180 || got[0].End != 360 {
		t.Fatalf("want single interval [180,360], got %+v", got)
	}
}

func TestShortSilenceBelowMinIgnored(t *testing.T) {
	d, _ := NewDetector(DetectorConfig{Threshold: 10, MinDurationTicks: clock.FromMs(5)})
	d.Observe(ev("a", 0, 90, []byte{1}), nil) // 1 ms only
	if got := d.Flush(); len(got) != 0 {
		t.Fatalf("sub-minimum silence must be suppressed, got %+v", got)
	}
}

func TestSilenceDoesNotCrossErrorRange(t *testing.T) {
	d, _ := NewDetector(DetectorConfig{Threshold: 10, MinDurationTicks: 0})
	errs := []clock.RangeError{{Kind: clock.KindDiscontinuity, Start: 180, End: 360, Msg: "gap"}}
	d.Observe(ev("a", 0, 90, []byte{1}), nil)
	d.Observe(ev("a", 90, 180, []byte{1}), nil)
	d.Observe(ev("a", 180, 270, []byte{1}), errs) // overlaps the error range
	d.Observe(ev("a", 360, 450, []byte{1}), nil)  // still blocked until loud sample
	got := d.Flush()
	if len(got) != 1 || got[0].End > 180 {
		t.Fatalf("window must close at the error boundary, got %+v", got)
	}
	if len(d.Errors()) != 1 || d.Errors()[0].Kind != clock.KindDiscontinuity {
		t.Fatalf("error must be shared into detector diagnostics: %+v", d.Errors())
	}
}

func TestSilenceResumesAfterLoudSample(t *testing.T) {
	// Minimum two packets: the pre-error window is only one packet and gets
	// suppressed; the sustained silence after recovery is reported.
	d, _ := NewDetector(DetectorConfig{Threshold: 10, MinDurationTicks: 180})
	errs := []clock.RangeError{{Kind: clock.KindContinuity, Start: 90, End: 180, Msg: "jump"}}
	d.Observe(ev("a", 0, 90, []byte{1}), nil)
	d.Observe(ev("a", 90, 180, []byte{1}), errs)
	d.Observe(ev("a", 180, 270, []byte{200}), nil) // loud clears the blocked flag
	d.Observe(ev("a", 270, 360, []byte{1}), nil)
	d.Observe(ev("a", 360, 450, []byte{1}), nil)
	got := d.Flush()
	if len(got) != 1 || got[0].Start != 270 || got[0].End != 450 {
		t.Fatalf("silence must resume after loud sample, got %+v", got)
	}
}

func TestDetectorConfigValidation(t *testing.T) {
	if _, err := NewDetector(DetectorConfig{Threshold: 300}); err == nil {
		t.Fatal("threshold above 255 must be rejected")
	}
	if _, err := NewDetector(DetectorConfig{MinDurationTicks: -1}); err == nil {
		t.Fatal("negative min duration must be rejected")
	}
	if _, err := NewDetector(DetectorConfig{Threshold: 0, MinDurationTicks: 0}); err != nil {
		t.Fatalf("zero config must be valid, got %v", err)
	}
}

func TestEmptyPayloadIsSilent(t *testing.T) {
	d, _ := NewDetector(DetectorConfig{Threshold: 0, MinDurationTicks: 90})
	d.Observe(ev("a", 0, 90, nil), nil)
	if got := d.Flush(); len(got) != 1 {
		t.Fatalf("empty payload must count as silence, got %+v", got)
	}
}
