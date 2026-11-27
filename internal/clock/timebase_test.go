package clock

import (
	"strings"
	"testing"

	"media.rebuild/internal/model"
)

func TestFrameStartExact(t *testing.T) {
	cases := []struct {
		rate  Rate
		frame int64
		want  int64
	}{
		{Rate25, 0, 0},
		{Rate25, 25, 1_000_000_000},
		{Rate25, 50, 2_000_000_000},
		{Rate24, 24, 1_000_000_000},
		{Rate50, 150, 3_000_000_000},
		{Rate2997, 30000, 1_001_000_000_000},
		{Rate2997, 1, 33_366_667},
	}
	for _, c := range cases {
		got, err := frameStartNs(c.rate, c.frame)
		if err != nil {
			t.Fatalf("%s frame %d: %v", c.rate, c.frame, err)
		}
		if got != c.want {
			t.Errorf("%s frame %d = %d, want %d", c.rate, c.frame, got, c.want)
		}
	}
}

func TestFrameStartRejectsNegativeAndOverflow(t *testing.T) {
	if _, err := frameStartNs(Rate25, -1); !model.IsCode(err, model.CodeTimecodeFrame) {
		t.Fatalf("negative frame: got %v", err)
	}
	if _, err := frameStartNs(Rate{Num: 1, Den: 1}, 1<<62); !model.IsCode(err, model.CodeOverflow) {
		t.Fatalf("overflow: got %v", err)
	}
	if _, err := frameStartNs(Rate{Num: 0, Den: 1}, 1); !model.IsCode(err, model.CodeTimecodeRate) {
		t.Fatalf("zero rate: got %v", err)
	}
}

func TestPerfectRateRoundTrip(t *testing.T) {
	for _, r := range []Rate{Rate24, Rate25, Rate50, Rate2997} {
		var frame int64
		for frame = 0; frame < 400; frame++ {
			ns, err := frameStartNs(r, frame)
			if err != nil {
				t.Fatal(err)
			}
			back, err := FrameAt(r, ns)
			if err != nil {
				t.Fatal(err)
			}
			if back != frame {
				t.Fatalf("%s: frame %d -> %d -> %d", r, frame, ns, back)
			}
		}
	}
}

func TestFrameAtIsFloor(t *testing.T) {
	// At 1 ms per frame, 999999 ns is still frame 0 and 1e6 ns frame 1.
	r := Rate{Num: 1000, Den: 1}
	if f, _ := FrameAt(r, 999_999); f != 0 {
		t.Fatalf("got %d", f)
	}
	if f, _ := FrameAt(r, 1_000_000); f != 1 {
		t.Fatalf("got %d", f)
	}
	// Large timestamps must not wrap.
	const big = 1_000_000_000_000_000
	if f, err := FrameAt(Rate25, big); err != nil || f != big/40_000_000 {
		t.Fatalf("big: %d %v", f, err)
	}
}

func TestParseRate(t *testing.T) {
	for in, want := range map[string]string{
		"24": "24/1", "25": "25/1", "29.97": "30000/1001",
		"50": "50/1", "23.976": "24000/1001", "60000/2002": "30000/1001",
	} {
		got, err := ParseRate(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got.String() != want {
			t.Errorf("%q = %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"", "0", "-25", "30/0", "1/100000", "abc"} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseTimecodeNonDrop(t *testing.T) {
	tc, err := ParseTimecode("01:02:03:04", Rate25)
	if err != nil {
		t.Fatal(err)
	}
	if tc.FrameNumber() != ((3600+120+3)*25 + 4) {
		t.Fatalf("frame number %d", tc.FrameNumber())
	}
	ns, err := tc.TimestampNs()
	if err != nil {
		t.Fatal(err)
	}
	if ns != ((3600+120+3)*1_000_000_000 + 4*40_000_000) {
		t.Fatalf("ns %d", ns)
	}
}

func TestParseTimecodeRejects(t *testing.T) {
	cases := []struct {
		in   string
		rate Rate
		code model.Code
	}{
		{"01:02:03", Rate25, model.CodeTimecodeSyntax},
		{"01:02:03:75", Rate25, model.CodeTimecodeFrame},
		{"01:02:63:00", Rate25, model.CodeTimecodeFrame},
		{"01:62:03:00", Rate25, model.CodeTimecodeFrame},
		{"00:00:00;00", Rate25, model.CodeTimecodeDropFrame},
		{"00:01:00;00", Rate2997, model.CodeTimecodeDropFrame},
		{"00:01:00;01", Rate2997, model.CodeTimecodeDropFrame},
		{"aa:00:00:00", Rate25, model.CodeTimecodeSyntax},
		{"1:02:03:04", Rate25, model.CodeTimecodeSyntax},
	}
	for _, c := range cases {
		_, err := ParseTimecode(c.in, c.rate)
		if err == nil {
			t.Errorf("%q accepted", c.in)
			continue
		}
		if c.code != "" && !model.IsCode(err, c.code) {
			t.Errorf("%q: code %s want %s (%v)", c.in, model.CodeOf(err), c.code, err)
		}
		if !strings.Contains(err.Error(), "timecode") {
			t.Errorf("%q error lacks source: %v", c.in, err)
		}
	}
}

func TestDropFrameFrameNumber(t *testing.T) {
	// One minute of DF timecode = 1798 frames but labels jump 00 -> 02.
	tc, err := ParseTimecode("00:01:00;02", Rate2997)
	if err != nil {
		t.Fatal(err)
	}
	if tc.FrameNumber() != 1800 {
		t.Fatalf("frame number %d, want 1800", tc.FrameNumber())
	}
	// Ten minutes = 17982 frames, no drop at the tenth minute.
	tc, err = ParseTimecode("00:10:00;00", Rate2997)
	if err != nil {
		t.Fatal(err)
	}
	if tc.FrameNumber() != 17982 {
		t.Fatalf("frame number %d, want 17982", tc.FrameNumber())
	}
}

func TestFormatFrameRoundTrip(t *testing.T) {
	for _, drop := range []bool{false, true} {
		rate := Rate25
		if drop {
			rate = Rate2997
		}
		for frame := int64(0); frame < 20000; frame += 7 {
			s, err := FormatFrame(rate, frame, drop)
			if err != nil {
				t.Fatalf("%s %d: %v", rate, frame, err)
			}
			tc, err := ParseTimecode(s, rate)
			if err != nil {
				t.Fatalf("%s %d -> %q: %v", rate, frame, s, err)
			}
			if tc.FrameNumber() != frame {
				t.Fatalf("%s frame %d -> %q -> %d", rate, frame, s, tc.FrameNumber())
			}
			ns, _ := tc.TimestampNs()
			want, _ := frameStartNs(rate, frame)
			if ns != want {
				t.Fatalf("%s frame %d: ns %d want %d", rate, frame, ns, want)
			}
		}
	}
	if _, err := FormatFrame(Rate25, 0, true); !model.IsCode(err, model.CodeTimecodeDropFrame) {
		t.Fatalf("25fps DF accepted: %v", err)
	}
	if _, err := FormatFrame(Rate25, -1, false); !model.IsCode(err, model.CodeTimecodeFrame) {
		t.Fatalf("negative accepted: %v", err)
	}
}

func TestNoFloatAccumulation(t *testing.T) {
	// 29.97 for one hour must be exactly 3600 seconds of frame instants, not
	// 3601.8 or a value that drifts with repeated additions.
	tc, err := ParseTimecode("01:00:00;00", Rate2997)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := tc.TimestampNs()
	if err != nil {
		t.Fatal(err)
	}
	// 107892 frames at 1001/30000 s = 3.5999964e12 ns.
	if ns != 3_599_996_400_000 {
		t.Fatalf("ns %d", ns)
	}
}

func TestValidateSpan(t *testing.T) {
	if err := ValidateSpan(5, 4); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("inverted span: %v", err)
	}
	if err := ValidateSpan(-1, 4); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("negative span: %v", err)
	}
	if err := ValidateSpan(4, 4); err != nil {
		t.Fatalf("empty span rejected: %v", err)
	}
}
