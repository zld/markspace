package media

import (
	"errors"
	"strings"
	"testing"
)

func TestRatArithmetic(t *testing.T) {
	a := NewRat(1, 3)
	b := NewRat(1, 6)
	if got := a.Add(b).String(); got != "1/2" {
		t.Fatalf("1/3+1/6 = %s, want 1/2", got)
	}
	if got := a.Sub(b).String(); got != "1/6" {
		t.Fatalf("1/3-1/6 = %s, want 1/6", got)
	}
	if got := NewRat(4, 8).String(); got != "1/2" {
		t.Fatalf("4/8 reduced = %s, want 1/2", got)
	}
	if a.Cmp(b) <= 0 {
		t.Fatalf("1/3 must compare greater than 1/6")
	}
	if got := a.Mul(b).String(); got != "1/18" {
		t.Fatalf("1/3*1/6 = %s, want 1/18", got)
	}
	if got := a.Quo(b).String(); got != "2" {
		t.Fatalf("1/3/1/6 = %s, want 2", got)
	}
}

func TestParseSRTAndVTT(t *testing.T) {
	got, err := ParseSRTTime("00:00:01,500")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "3/2" {
		t.Fatalf("SRT 1.5s = %s", got)
	}
	got, err = ParseVTTTime("01:02:03.250")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(NewRat(3723250, 1000)) != 0 {
		t.Fatalf("VTT = %s", got)
	}
}

func TestInvalidTimesAreRejected(t *testing.T) {
	bad := []string{
		"00:60:01,000", // minute out of range
		"00:00:61,000", // second out of range
		"00:00:01.50",  // wrong fraction width for SRT shape
		"12:34",        // too few fields
		"00:00:00,abc", // non-numeric
	}
	for _, in := range bad {
		if _, err := ParseSRTTime(in); err == nil {
			t.Fatalf("SRT %q accepted, want error", in)
		} else if me, ok := AsError(err); !ok || me.Code != CodeInvalidTime {
			t.Fatalf("SRT %q error = %v, want CodeInvalidTime", in, err)
		}
	}
	if _, err := ParseVTTTime("00:00:61.000"); err == nil {
		t.Fatal("VTT invalid second accepted")
	}
}

func TestTimecodeRates(t *testing.T) {
	cases := []struct {
		in   string
		rate FrameRate
		want string // exact seconds as Rat string
	}{
		{"00:00:00:23", Rate24, "23/24"},
		{"00:00:01:00", Rate25, "1"},
		{"00:00:00:49", Rate50, "49/50"},
		{"00:00:10:00", Rate2997, "1001/100"},
		{"00:00:00:29", Rate2997, "29029/30000"},
	}
	for _, c := range cases {
		tc, err := ParseTimecode(c.in, c.rate)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if tc.Start.String() != c.want {
			t.Fatalf("%s = %s, want %s", c.in, tc.Start, c.want)
		}
	}
}

func TestDropFrameTimecode(t *testing.T) {
	// 00:01:00;02 skips frames 0 and 1 -> frame 1798 of the stream.
	tc, err := ParseTimecode("00:01:00;02", Rate2997)
	if err != nil {
		t.Fatal(err)
	}
	// 00:01:00;02 is the first legal frame after the minute: its real frame
	// index is 1800 (NDF base 1802 minus the 2 dropped frame numbers), which
	// lands at exactly 60.06 s on the 30000/1001 media time line.
	want := NewRat(1800, 1).Quo(Rate2997.Rat)
	if want.String() != "3003/50" {
		t.Fatalf("test setup wrong: %s", want)
	}
	if tc.Start.Cmp(want) != 0 || !tc.DropFrame {
		t.Fatalf("DF 00:01:00;02 = %s df=%v, want %s", tc.Start, tc.DropFrame, want)
	}
	// Dropped frame numbers must be rejected, never folded over.
	if _, err := ParseTimecode("00:01:00;00", Rate2997); err == nil {
		t.Fatal("dropped frame number 00 accepted")
	}
	// DF is only valid at 29.97.
	if _, err := ParseTimecode("00:00:00;00", Rate25); err == nil {
		t.Fatal("drop frame accepted at 25 fps")
	}
	// Frame beyond the rate is rejected.
	if _, err := ParseTimecode("00:00:00:30", Rate2997); err == nil {
		t.Fatal("frame 30 accepted at ~30 fps")
	}
}

func TestMediaTimeZeroTimescale(t *testing.T) {
	if _, err := MediaTime(100, 0); err == nil {
		t.Fatal("zero timescale accepted")
	}
}

func TestDigestKindsAreDistinct(t *testing.T) {
	p := DigestBytes(KindPayload, []byte("same bytes"))
	c := DigestBytes(KindContainer, []byte("same bytes"))
	if p == c {
		t.Fatal("payload and container digests must differ")
	}
	// Path and mtime never enter the hash: same content, different call site.
	if DigestBytes(KindPayload, []byte("x")) != DigestBytes(KindPayload, []byte("x")) {
		t.Fatal("identical content produced different digests")
	}
	if !strings.HasPrefix(p, "payload:") || len(strings.TrimPrefix(p, "payload:")) != 64 {
		t.Fatalf("digest shape wrong: %s", p)
	}
}

func TestLimitedReader(t *testing.T) {
	_, err := ReadAllLimited(strings.NewReader("0123456789"), 4)
	if err == nil {
		t.Fatal("oversize input accepted")
	}
	var me *Error
	if !errors.As(err, &me) || me.Code != CodeOversize {
		t.Fatalf("err = %v, want CodeOversize", err)
	}
	got, err := ReadAllLimited(strings.NewReader("0123"), 4)
	if err != nil || string(got) != "0123" {
		t.Fatalf("within-limit read failed: %v %q", err, got)
	}
}
