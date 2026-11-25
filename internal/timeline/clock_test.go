package timeline

import (
	"errors"
	"testing"
)

func TestClockConvertsToNanoseconds(t *testing.T) {
	cases := []struct {
		timescale uint64
		ts        uint64
		want      Nanosecond
	}{
		{1_000_000_000, 0, 0},
		{1_000_000_000, 1, 1},
		{1_000_000_000, 1_500_000_000, 1_500_000_000},
		{48_000, 48_000, 1_000_000_000},
		{48_000, 24_000, 500_000_000},
		{90_000, 9_000, 100_000_000},
		{1_000, 1, 1_000_000},
		// A 32-bit timescale with a 64-bit timestamp must not overflow.
		{4_294_967_295, 4_294_967_295, 1_000_000_000},
	}
	for _, tc := range cases {
		c, err := NewClock(tc.timescale)
		if err != nil {
			t.Fatalf("clock %d: %v", tc.timescale, err)
		}
		if got := c.ToNanos(tc.ts); got != tc.want {
			t.Fatalf("clock %d ts %d = %d, want %d", tc.timescale, tc.ts, got, tc.want)
		}
	}
}

func TestClockRejectsZeroAndOversizedTimescale(t *testing.T) {
	if _, err := NewClock(0); CategoryOf(err) != CatIllegalLength {
		t.Fatalf("zero timescale: %v", err)
	}
	if _, err := NewClock(1 << 33); CategoryOf(err) != CatLimitExceeded {
		t.Fatalf("oversized timescale: %v", err)
	}
}

func TestClockRoundTrip(t *testing.T) {
	c := MustClock(90_000)
	for _, ns := range []Nanosecond{0, 1, 1_000_000, 1_234_567_890, 86_400_000_000_000} {
		ts := c.FromNanos(ns)
		back := c.ToNanos(ts)
		if DeltaNS(back, ns) > MaxTolerance {
			t.Fatalf("ns %d round-tripped to %d (delta %d)", ns, back, DeltaNS(back, ns))
		}
	}
}

func TestToleranceIsOneMillisecond(t *testing.T) {
	if !WithinTolerance(1_000_000, 0) {
		t.Fatal("exactly 1ms must be within tolerance")
	}
	if !WithinTolerance(0, 1_000_000) {
		t.Fatal("tolerance must be symmetric")
	}
	if WithinTolerance(1_000_001, 0) {
		t.Fatal("1ms + 1ns must exceed tolerance")
	}
	if DeltaNS(-5, 5) != 10 {
		t.Fatalf("delta = %d", DeltaNS(-5, 5))
	}
}

func TestFormatNSSaturatesAndPads(t *testing.T) {
	cases := map[Nanosecond]string{
		0:              "0.000000000",
		1:              "0.000000001",
		1_000_000:      "0.001000000",
		1_500_000_000:  "1.500000000",
		-2_500_000_000: "-2.500000000",
	}
	for ns, want := range cases {
		if got := FormatNS(ns); got != want {
			t.Fatalf("FormatNS(%d) = %q, want %q", ns, got, want)
		}
	}
}

func TestErrorExitCodes(t *testing.T) {
	cases := []struct {
		cat  Category
		code int
	}{
		{CatTruncated, ExitBadInput},
		{CatIllegalLength, ExitBadInput},
		{CatTrackConflict, ExitBadInput},
		{CatCheckpoint, ExitCheckpoint},
		{CatCheckpointState, ExitCheckpoint},
		{CatIO, ExitLocalIO},
	}
	for _, tc := range cases {
		e := New(StageParse, tc.cat, 0xAE, 42, "boom")
		if got := e.ExitCode(); got != tc.code {
			t.Fatalf("%s exit = %d, want %d", tc.cat, got, tc.code)
		}
	}
	if ExitCodeOf(nil) != ExitOK {
		t.Fatal("nil error must exit 0")
	}
	if ExitCodeOf(errors.New("plain")) != ExitBadInput {
		t.Fatal("unclassified error must exit as bad input")
	}
}

func TestErrorCarriesOffsetElementTracksField(t *testing.T) {
	e := New(StageValidate, CatTrackConflict, 0xAE, 116, "conflict")
	e = e.WithTracks(1, 6).WithField("Video.Colour")
	if e.Offset != 116 || e.ElementID != 0xAE {
		t.Fatalf("location lost: %+v", e)
	}
	if len(e.Tracks) != 2 || e.Tracks[0] != 1 {
		t.Fatalf("tracks = %v", e.Tracks)
	}
	if OffsetOf(e) != 116 || CategoryOf(e) != CatTrackConflict {
		t.Fatalf("accessors disagree: %d %s", OffsetOf(e), CategoryOf(e))
	}
	msg := e.Error()
	for _, part := range []string{"0xAE", "offset=116", "tracks=[1 6]", "field=Video.Colour"} {
		if !contains(msg, part) {
			t.Fatalf("message %q missing %q", msg, part)
		}
	}
}

func TestWrapKeepsCause(t *testing.T) {
	cause := errors.New("disk gone")
	e := Wrap(cause, StageWrite, CatIO, 0, -1, "write manifest")
	if !errors.Is(e, cause) {
		t.Fatal("wrapped cause not reachable")
	}
	if e.ExitCode() != ExitLocalIO {
		t.Fatalf("exit = %d", e.ExitCode())
	}
}

func TestRangeValidity(t *testing.T) {
	if r := (Range{Start: 10, End: 20}); !r.Valid() || r.Duration() != 10 {
		t.Fatalf("range: %+v", r)
	}
	if r := (Range{Start: 20, End: 10}); r.Duration() != 0 {
		t.Fatalf("reversed duration = %d", r.Duration())
	}
	if r := (Range{Start: -1, End: 10}); r.Valid() {
		t.Fatal("negative start must be invalid")
	}
}

func contains(hay, needle string) bool {
	if len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
