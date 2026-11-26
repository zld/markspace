package media

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestClockExactConversions(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"opus", must(FromOpusSamples(960)), 960 * 147},
		{"aac", must(FromAACSamples(1024)), 1024 * 160},
		{"millisecond", must(FromMilliseconds(1)), 7056},
		{"one second opus", must(FromOpusSamples(48000)), TicksPerSecond},
		{"one second aac", must(FromAACSamples(44100)), TicksPerSecond},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestClockOverflow(t *testing.T) {
	if _, err := CheckedMul(math.MaxInt64, 2); err == nil {
		t.Fatal("expected multiplication overflow")
	}
	if _, err := CheckedAdd(math.MaxInt64, 1); err == nil {
		t.Fatal("expected addition overflow")
	}
	if _, err := FromMilliseconds(math.MaxInt64); err == nil {
		t.Fatal("expected millisecond conversion overflow")
	}
}

func TestFaultCoordinatesAndExitCode(t *testing.T) {
	offset := int64(128)
	idx := 7
	err := InputError("ingest.rtp", "opus", errors.New("bad packet")).
		WithPacket(idx).WithOffset(offset)
	if code := ExitCode(err); code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
	msg := err.Error()
	for _, want := range []string{"phase=ingest.rtp", `track="opus"`, "packet_index=7", "byte_offset=128", "exit=2"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
	if ExitCode(QualityError("merge", "subs", errors.New("drift"))) != 3 {
		t.Fatal("quality exit code")
	}
	if ExitCode(BudgetError("pipeline", errors.New("units"))) != 4 {
		t.Fatal("budget exit code")
	}
	if ExitCode(CanceledError("pipeline", "opus")) != 130 {
		t.Fatal("cancel exit code")
	}
}

func must(v int64, err error) int64 {
	if err != nil {
		panic(err)
	}
	return v
}
