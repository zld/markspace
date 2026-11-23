package clock

import (
	"testing"

	"mediqc/internal/fault"
)

func TestTicksPerFrameExact(t *testing.T) {
	for _, r := range Supported() {
		if Timescale*int64(r.Den)%int64(r.Num) != 0 {
			t.Fatalf("rate %s does not divide timescale", r)
		}
	}
}

func TestTimecodeRoundtrip(t *testing.T) {
	cases := []struct {
		rate Rate
		drop bool
	}{
		{FPS24, false}, {FPS25, false}, {FPS50, false},
		{FPS2997, false}, {FPS2997, true},
	}
	for _, c := range cases {
		last, err := TimecodeToFrames(Timecode{HH: 23, MM: 59, SS: 59, FF: c.rate.Nominal() - 1, Drop: c.drop, Rate: c.rate})
		if err != nil {
			t.Fatal(err)
		}
		limit := last + 1 // real frames in one day at this rate
		for frames := int64(0); frames < limit; frames += 977 {
			tc, err := FramesToTimecode(frames, c.rate, c.drop)
			if err != nil {
				t.Fatalf("rate %s drop=%v frame %d: %v", c.rate, c.drop, frames, err)
			}
			back, err := TimecodeToFrames(tc)
			if err != nil {
				t.Fatalf("rate %s tc %s: %v", c.rate, tc, err)
			}
			if back != frames {
				t.Fatalf("rate %s drop=%v: frame %d -> %s -> %d", c.rate, c.drop, frames, tc, back)
			}
		}
	}
}

func TestTickRoundtripWithinOneFrame(t *testing.T) {
	for _, r := range Supported() {
		for frames := int64(0); frames < 100000; frames += 613 {
			got := TicksToFrames(FramesToTicks(frames, r), r)
			if d := got - frames; d < -1 || d > 1 {
				t.Fatalf("rate %s frame %d roundtrip off by %d", r, frames, d)
			}
		}
	}
}

func TestDropFrameLabels(t *testing.T) {
	// 00:01:00;02 is the first valid label of minute 1 in DF.
	tc := Timecode{MM: 1, SS: 0, FF: 2, Drop: true, Rate: FPS2997}
	frames, err := TimecodeToFrames(tc)
	if err != nil {
		t.Fatal(err)
	}
	if frames != 1800 { // 60s*30 nominal labels, labels 00/01 of minute 1 dropped
		t.Fatalf("00:01:00;02 should be real frame 1800, got %d", frames)
	}
	// Illegal dropped labels.
	for _, ff := range []int{0, 1} {
		bad := Timecode{MM: 1, SS: 0, FF: ff, Drop: true, Rate: FPS2997}
		if _, err := TimecodeToFrames(bad); !fault.IsCode(err, fault.CodeBadDropFrame) {
			t.Fatalf("ff=%d: want E_BAD_DF_MARKER, got %v", ff, err)
		}
	}
	// Minute 10 keeps labels 00/01.
	ok := Timecode{MM: 10, SS: 0, FF: 0, Drop: true, Rate: FPS2997}
	if _, err := TimecodeToFrames(ok); err != nil {
		t.Fatalf("minute 10 label 00 must be valid: %v", err)
	}
}

func TestDayBoundaryRejected(t *testing.T) {
	tc := Timecode{HH: 24, Rate: FPS25}
	if _, err := TimecodeToFrames(tc); !fault.IsCode(err, fault.CodeBadTimecode) {
		t.Fatalf("want E_BAD_TIMECODE for HH=24, got %v", err)
	}
}

func TestDropOnUnsupportedRate(t *testing.T) {
	tc := Timecode{MM: 1, FF: 5, Drop: true, Rate: FPS25}
	if _, err := TimecodeToFrames(tc); !fault.IsCode(err, fault.CodeUnsupportedRate) {
		t.Fatalf("want E_UNSUPPORTED_RATE, got %v", err)
	}
}

func TestParseTimecode(t *testing.T) {
	tc, err := ParseTimecode("01:02:03;04", FPS2997)
	if err != nil || !tc.Drop || tc.HH != 1 || tc.FF != 4 {
		t.Fatalf("got %v %v", tc, err)
	}
	if _, err := ParseTimecode("01:02:03", FPS25); !fault.IsCode(err, fault.CodeBadTimecode) {
		t.Fatalf("want E_BAD_TIMECODE, got %v", err)
	}
}
