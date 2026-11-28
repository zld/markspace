package timebase

import (
	"math"
	"testing"

	"mediarc/model"
)

func mustRate(t *testing.T, text string) Rate {
	t.Helper()
	r, err := ParseRate(text)
	if err != nil {
		t.Fatalf("ParseRate(%q): %v", text, err)
	}
	return r
}

func mustTC(t *testing.T, text string, rate Rate) int64 {
	t.Helper()
	tc, err := ParseTimecode(text, rate)
	if err != nil {
		t.Fatalf("ParseTimecode(%q): %v", text, err)
	}
	ns, err := rate.ToNanos(tc)
	if err != nil {
		t.Fatalf("ToNanos(%s): %v", tc, err)
	}
	return ns
}

func TestParseRateForms(t *testing.T) {
	cases := []struct {
		text string
		want Rate
	}{
		{"24", Rate{Num: 24, Den: 1}},
		{"25", Rate{Num: 25, Den: 1}},
		{"50", Rate{Num: 50, Den: 1}},
		{"29.97", Rate{Num: 30000, Den: 1001}},
		{"29.97ndf", Rate{Num: 30000, Den: 1001}},
		{"29.97df", Rate{Num: 30000, Den: 1001, DropFrame: true}},
		{" 25 ", Rate{Num: 25, Den: 1}},
	}
	for _, tc := range cases {
		got, err := ParseRate(tc.text)
		if err != nil {
			t.Fatalf("ParseRate(%q): %v", tc.text, err)
		}
		if got != tc.want {
			t.Fatalf("ParseRate(%q) = %+v, want %+v", tc.text, got, tc.want)
		}
	}
}

func TestParseRateErrors(t *testing.T) {
	for _, text := range []string{"30df", "24df", "60", "abc", "29.97xd", ""} {
		if _, err := ParseRate(text); err == nil {
			t.Fatalf("ParseRate(%q) succeeded, want error", text)
		}
	}
	if _, err := ParseRate("24df"); !model.IsCode(err, model.CodeIllegalDropFrame) {
		t.Fatalf("ParseRate(24df) error = %v, want illegal_drop_frame", err)
	}
}

func TestTimecodeToNanosExact(t *testing.T) {
	cases := []struct {
		rate string
		tc   string
		want int64
	}{
		{"24", "00:00:00:00", 0},
		{"24", "00:00:01:00", 1_000_000_000},
		{"24", "00:00:00:01", 41_666_667},
		{"25", "00:01:00:00", 60_000_000_000},
		{"50", "00:00:00:49", 980_000_000},
		{"29.97ndf", "00:01:00:00", 60_060_000_000},   // 1800 frames * 1001/30000 s
		{"29.97df", "00:01:00:02", 60_060_000_000},    // DF drops labels :00/:01, so :02 is frame 1800
		{"29.97df", "01:00:00:00", 3_599_996_400_000}, // 107892 real frames
	}
	for _, tc := range cases {
		rate := mustRate(t, tc.rate)
		got := mustTC(t, tc.tc, rate)
		if got != tc.want {
			t.Fatalf("%s @ %s = %d, want %d", tc.tc, tc.rate, got, tc.want)
		}
	}
}

func TestDropFrameFrameIndexSemantics(t *testing.T) {
	rate := mustRate(t, "29.97df")
	// 10 minutes of DF timecode is exactly 17982 real frames.
	tc, err := ParseTimecode("00:10:00:00", rate)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := rate.ToNanos(tc)
	if err != nil {
		t.Fatal(err)
	}
	oneFrame, err := rate.FrameNanos()
	if err != nil {
		t.Fatal(err)
	}
	frames, err := rate.NanosToFrames(ns)
	if err != nil {
		t.Fatal(err)
	}
	if frames != 17982 {
		t.Fatalf("10 minutes = %d frames, want 17982", frames)
	}
	if want := int64(599_999_400_000); ns != want {
		t.Fatalf("10 minutes = %d ns, want %d", ns, want)
	}
	if oneFrame < 33_366_666 || oneFrame > 33_366_667 {
		t.Fatalf("one 29.97 frame = %d ns", oneFrame)
	}
}

func TestDropFrameIllegalLabels(t *testing.T) {
	rate := mustRate(t, "29.97df")
	for _, text := range []string{"00:01:00:00", "00:01:00:01", "00:12:00:01"} {
		tc, err := ParseTimecode(text, rate)
		if err != nil {
			t.Fatalf("ParseTimecode(%q): %v", text, err)
		}
		if _, err := rate.ToNanos(tc); !model.IsCode(err, model.CodeIllegalDropFrame) {
			t.Fatalf("%q error = %v, want illegal_drop_frame", text, err)
		}
	}
	// Every tenth minute is exempt from the drop.
	if _, err := rate.ToNanos(Timecode{Hours: 0, Minutes: 10, Seconds: 0, Frames: 0}); err != nil {
		t.Fatalf("00:10:00:00 should be legal: %v", err)
	}
}

func TestDropFrameSeparatorOnNonDropRate(t *testing.T) {
	rate := mustRate(t, "25")
	if _, err := ParseTimecode("00:00:01;00", rate); !model.IsCode(err, model.CodeIllegalDropFrame) {
		t.Fatalf("semicolon on 25fps: %v", err)
	}
}

func TestTimecodeValidation(t *testing.T) {
	cases := []struct {
		rate string
		tc   string
		code model.Code
	}{
		{"25", "00:00:00:25", model.CodeInvalidTimecode},
		{"25", "00:60:00:00", model.CodeInvalidTimecode},
		{"25", "00:00:60:00", model.CodeInvalidTimecode},
		{"25", "1:2", model.CodeInvalidTimecode},
		{"25", "00:00:00:ab", model.CodeInvalidTimecode},
		{"25", "00:00:00:100", model.CodeInvalidTimecode},
	}
	for _, tc := range cases {
		rate := mustRate(t, tc.rate)
		parsed, err := ParseTimecode(tc.tc, rate)
		if err != nil {
			if !model.IsCode(err, tc.code) {
				t.Fatalf("%q error = %v, want %s", tc.tc, err, tc.code)
			}
			continue
		}
		if _, err := rate.ToNanos(parsed); !model.IsCode(err, tc.code) {
			t.Fatalf("%q ToNanos error = %v, want %s", tc.tc, err, tc.code)
		}
	}
}

func TestNegativeTimecodeStable(t *testing.T) {
	rate := mustRate(t, "25")
	_, err := rate.ToNanos(Timecode{Frames: -1})
	if !model.IsCode(err, model.CodeNegativeTime) {
		t.Fatalf("negative frames: %v", err)
	}
	if _, err := rate.NanosToFrames(-1); !model.IsCode(err, model.CodeNegativeTime) {
		t.Fatalf("negative nanos: %v", err)
	}
	if _, err := ParseClock("-00:00:01.000"); !model.IsCode(err, model.CodeNegativeTime) {
		t.Fatalf("negative clock: %v", err)
	}
}

func TestOverflowStable(t *testing.T) {
	rate := mustRate(t, "24")
	_, err := rate.ToNanos(Timecode{Hours: math.MaxInt64 / 3600, Minutes: 59})
	if !model.IsCode(err, model.CodeTimestampOverflow) {
		t.Fatalf("overflowing timecode: %v", err)
	}
	if _, err := NanosFromScale(math.MaxInt64, 1); !model.IsCode(err, model.CodeTimestampOverflow) {
		t.Fatalf("NanosFromScale overflow: %v", err)
	}
	if _, err := NanosToScale(math.MaxInt64, 48000); !model.IsCode(err, model.CodeTimestampOverflow) {
		t.Fatalf("NanosToScale overflow: %v", err)
	}
	huge, err := NewRate(math.MaxInt64, 1, false)
	if err != nil {
		t.Fatalf("NewRate: %v", err)
	}
	if _, err := huge.NanosToFrames(1 << 62); !model.IsCode(err, model.CodeTimestampOverflow) {
		t.Fatalf("NanosToFrames overflow: %v", err)
	}
}
