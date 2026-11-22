package clock

import (
	"math"
	"testing"

	"mediaarchive/internal/diag"
)

func TestRationalExactArithmetic(t *testing.T) {
	if got := FromMillis(1500).Add(FromMillis(500)); !got.Equal(NewRat(2, 1)) {
		t.Fatalf("1.5s+0.5s = %v, want 2", got)
	}
	if got := NewRat(1001, 30000).MulInt(30000); !got.Equal(NewRat(1001, 1)) {
		t.Fatalf("frame arithmetic lost precision: %v", got)
	}
	if FromMicros(1).Den != 1000000 {
		t.Fatalf("microsecond base not preserved: %v", FromMicros(1))
	}
	if !NewRat(-4, 8).Equal(NewRat(-1, 2)) {
		t.Fatalf("reduction failed: %v", NewRat(-4, 8))
	}
	if NewRat(1, 0).Den != 1 {
		t.Fatal("zero denominator must be normalised")
	}
}

func TestDropFrameLabelArithmetic(t *testing.T) {
	df := Format2997DF
	cases := []struct {
		label string
		ticks int64 // frame number
	}{
		{"00:00:00:00", 0},
		{"00:00:59:29", 1799},
		{"00:01:00;02", 1800}, // 00:01:00;00 and ;01 do not exist
		{"00:10:00:00", 17982},
		{"01:00:00:00", 107892},
	}
	for _, c := range cases {
		tc, err := ParseTimecode(c.label, df)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		got, err := tc.FrameNumber()
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		if got != c.ticks {
			t.Fatalf("%s frame = %d, want %d", c.label, got, c.ticks)
		}
	}
	// A label naming a dropped frame must fail, not silently shift.
	if _, err := ParseTimecode("00:01:00;00", df); !diag.IsCode(err, diag.CodeInvalidTime) {
		t.Fatalf("dropped label accepted: %v", err)
	}
}

func TestDropFrameWallClockAccuracy(t *testing.T) {
	df := Format2997DF
	oneHour, err := ParseTimecode("01:00:00:00", df)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := oneHour.Position()
	if err != nil {
		t.Fatal(err)
	}
	// 107892 real frames at 1001/30000 is exactly 3599.9964 s.
	if diff := math.Abs(pos.Float() - 3599.9964); diff > 1e-9 {
		t.Fatalf("df hour = %.6f, want 3599.9964", pos.Float())
	}
	ndf := Format2997NDF
	oneHourNDF, _ := ParseTimecode("01:00:00:00", ndf)
	posNDF, _ := oneHourNDF.Position()
	if diff := math.Abs(posNDF.Float() - 3600.0*1001/1000); diff > 1e-9 {
		t.Fatalf("ndf hour = %.6f", posNDF.Float())
	}
}

func TestIntegerFormats(t *testing.T) {
	cases := []struct {
		f     Format
		label string
		want  float64
	}{
		{Format24, "00:00:01:00", 1},
		{Format25, "00:00:01:00", 1},
		{Format50, "00:00:01:00", 1},
		{Format24, "00:00:00:01", 1.0 / 24},
		{Format50, "00:00:00:49", 49.0 / 50},
	}
	for _, c := range cases {
		tc, err := ParseTimecode(c.label, c.f)
		if err != nil {
			t.Fatalf("%v %s: %v", c.f, c.label, err)
		}
		p, err := tc.Position()
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(p.Float()-c.want) > 1e-12 {
			t.Fatalf("%v %s = %v want %v", c.f, c.label, p.Float(), c.want)
		}
	}
	if _, err := ParseTimecode("00:00:00:24", Format24); !diag.IsCode(err, diag.CodeInvalidTime) {
		t.Fatal("frame 24 on 24fps accepted")
	}
	if _, err := ParseTimecode("00:60:00:00", Format25); !diag.IsCode(err, diag.CodeInvalidTime) {
		t.Fatal("minute 60 accepted")
	}
}

func TestExpandWrapsAtMidnight(t *testing.T) {
	f := Format25
	labels := []Timecode{
		{Hours: 23, Minutes: 59, Seconds: 59, Frames: 24},
		{Hours: 0, Minutes: 0, Seconds: 0, Frames: 0},
		{Hours: 0, Minutes: 0, Seconds: 1, Frames: 0},
	}
	frames, err := ExpandSequence(labels, f, 100)
	if err != nil {
		t.Fatal(err)
	}
	if frames[1]-frames[0] != 1 || frames[2]-frames[1] != 25 {
		t.Fatalf("wrap not expanded: %v", frames)
	}
	bad := []Timecode{{Hours: 0, Minutes: 0, Seconds: 5, Frames: 0}, {Hours: 0, Minutes: 0, Seconds: 4, Frames: 0}}
	_, err = ExpandSequence(bad, f, 7)
	if !diag.IsCode(err, diag.CodeTimestampRollback) {
		t.Fatalf("rollback accepted: %v", err)
	}
	if f, ok := diag.As(err); !ok || f.Offset != 8 {
		t.Fatalf("rollback offset = %+v", f)
	}
}

func TestUnwrapper32Bit(t *testing.T) {
	u := NewUnwrapper(32)
	seq := []int64{0xFFFFF000, 0xFFFFF800, 0x00000800, 0x00001000}
	var got []int64
	for _, v := range seq {
		n, err := u.Step(v)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("unfolded sequence not monotone: %v", got)
		}
	}
	if got[3]-got[0] != 0x2000 {
		t.Fatalf("delta across wrap = %d, want 8192", got[3]-got[0])
	}
	u2 := NewUnwrapper(32)
	_, _ = u2.Step(1000)
	if _, err := u2.Step(900); !diag.IsCode(err, diag.CodeTimestampRollback) {
		t.Fatalf("small rollback accepted: %v", err)
	}
}

func TestFormatResolution(t *testing.T) {
	if _, err := FormatFromRate("30000/1001", 0); !diag.IsCode(err, diag.CodeAmbiguousDF) {
		t.Fatalf("ambiguous rate accepted: %v", err)
	}
	if f, err := FormatFromRate("30000/1001df", 0); err != nil || f != Format2997DF {
		t.Fatalf("df rate: %v %v", f, err)
	}
	if f, err := FormatFromRate("25", 0); err != nil || f != Format25 {
		t.Fatalf("25: %v %v", f, err)
	}
	if f, err := ParseFormat("29.97"); err != nil || f != Format2997NDF {
		t.Fatalf("29.97 token: %v %v", f, err)
	}
}

func TestLabelFrameRoundTrip(t *testing.T) {
	for _, f := range []Format{Format24, Format25, Format2997DF, Format2997NDF, Format50} {
		day := f.DayFrames()
		if f.IsDropFrame() {
			day = 10 * 60 * 30 * 60 // one hour of labels for a tighter sweep
		}
		step := int64(1)
		if day > 50000 {
			step = 97
		}
		for frame := int64(0); frame < day; frame += step {
			tc, err := LabelFromFrame(frame, f)
			if err != nil {
				t.Fatalf("%v frame %d: %v", f, frame, err)
			}
			back, err := tc.FrameNumber()
			if err != nil {
				t.Fatalf("%v %s: %v", f, tc, err)
			}
			if back != frame {
				t.Fatalf("%v round trip %d -> %s -> %d", f, frame, tc, back)
			}
		}
	}
}

func TestLabelFromFrameDFKnownValues(t *testing.T) {
	cases := map[int64]string{
		0:      "00:00:00;00",
		1799:   "00:00:59;29",
		1800:   "00:01:00;02",
		17982:  "00:10:00;00",
		107892: "01:00:00;00",
	}
	for frame, want := range cases {
		tc, err := LabelFromFrame(frame, Format2997DF)
		if err != nil {
			t.Fatal(err)
		}
		if tc.String() != want {
			t.Fatalf("frame %d = %s, want %s", frame, tc, want)
		}
	}
}

func TestFormatForTimecodesRejectsAmbiguousRate(t *testing.T) {
	if _, err := FormatForTimecodes("30000/1001", 42); !diag.IsCode(err, diag.CodeAmbiguousDF) {
		t.Fatalf("expected ambiguous df fault, got %v", err)
	}
	f, err := FormatForTimecodes("30000/1001df", 42)
	if err != nil || f != Format2997DF {
		t.Fatalf("df rate: %v %v", f, err)
	}
}
