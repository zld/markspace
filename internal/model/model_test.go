package model

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestTimestampValidation(t *testing.T) {
	if err := Timestamp(0).Validate(); err != nil {
		t.Fatalf("zero timestamp rejected: %v", err)
	}
	err := Timestamp(-1).Validate()
	f, ok := AsFault(err)
	if !ok || f.Code != CodeTimeInvalid || f.Kind != KindInput {
		t.Fatalf("negative timestamp fault = %#v, %v", f, err)
	}
}

func TestAddSubOverflow(t *testing.T) {
	if _, err := AddNS(math.MaxInt64, 1); err == nil {
		t.Fatal("overflow not detected")
	} else if f, _ := AsFault(err); f.Code != CodeTimeOverflow {
		t.Fatalf("code = %s", f.Code)
	}
	if v, err := AddNS(math.MaxInt64-1, 1); err != nil || v != math.MaxInt64 {
		t.Fatalf("boundary add = %d, %v", v, err)
	}
	if _, err := SubNS(0, 1); err == nil {
		t.Fatal("negative result not detected")
	}
	if v, err := SubNS(5, 2); err != nil || v != 3 {
		t.Fatalf("sub = %d, %v", v, err)
	}
}

func TestClockRebaseAndOffset(t *testing.T) {
	c, err := NewClock(1_000)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := c.Rebase(250, 50)
	if err != nil || ts != 1_300 {
		t.Fatalf("rebase = %d, %v", ts, err)
	}
	if off, err := c.Offset(1_250); err != nil || off != 250 {
		t.Fatalf("offset = %d, %v", off, err)
	}
	if _, err := c.Offset(999); err == nil {
		t.Fatal("pre-epoch timestamp accepted")
	}
}

func TestRTPToNS(t *testing.T) {
	// 90000 Hz clock: one tick = 11111.11 ns.
	if ns, err := RTPToNS(90000, 90000); err != nil || ns != 1_000_000_000 {
		t.Fatalf("one second = %d, %v", ns, err)
	}
	if ns, err := RTPToNS(1, 90000); err != nil || ns != 11111 {
		t.Fatalf("one tick = %d, %v", ns, err)
	}
	if _, err := RTPToNS(1, 0); err == nil {
		t.Fatal("zero clock accepted")
	}
}

func TestFrameValidation(t *testing.T) {
	good := Frame{
		Track: 1, Sequence: 1, PTS: 10, Size: 4, Path: "t1/f1.bin",
		SHA256: strings.Repeat("a", 64),
		Color:  Color{Primaries: "bt709", Transfer: "bt709", Range: "full"},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good frame rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Frame)
		code   string
	}{
		{"track", func(f *Frame) { f.Track = 0 }, CodeTrackInvalid},
		{"seq", func(f *Frame) { f.Sequence = 0 }, CodeSeqInvalid},
		{"size", func(f *Frame) { f.Size = MaxFrameBytes + 1 }, CodeSizeBounds},
		{"path", func(f *Frame) { f.Path = "" }, CodePathInvalid},
		{"hash", func(f *Frame) { f.SHA256 = "xyz" }, CodeHashMalformed},
		{"color", func(f *Frame) { f.Color.Primaries = "bt2020" }, CodeColorInvalid},
	}
	for _, tc := range cases {
		f := good
		tc.mutate(&f)
		err := f.Validate()
		fa, ok := AsFault(err)
		if !ok || fa.Code != tc.code {
			t.Errorf("%s: got %v want %s", tc.name, err, tc.code)
		}
		if fa.Track != f.Track || fa.Frame != f.Sequence {
			t.Errorf("%s: fault location = %d/%d", tc.name, fa.Track, fa.Frame)
		}
	}
}

func TestFaultSerializes(t *testing.T) {
	f := NewFault(KindIngest, CodeHashMismatch, 2, 7, "digest differs")
	f.OffsetNS = -40
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var back Fault
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Track != 2 || back.Frame != 7 || back.Code != CodeHashMismatch || back.OffsetNS != -40 {
		t.Fatalf("round trip lost fields: %s", b)
	}
	if !strings.Contains(back.Error(), "track 2 frame 7") {
		t.Fatalf("location missing from %q", back.Error())
	}
}

func TestFaultsExitCodes(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("empty exit = %d", got)
	}
	rec := Faults{NewFault(KindIngest, CodeFrameMissing, 1, 3, "gap").RecoverableFault()}
	if got := ExitCode(rec); got != 0 {
		t.Fatalf("recoverable exit = %d", got)
	}
	hard := Faults{NewFault(KindIngest, CodePayloadCorrupt, 1, 3, "bad")}
	if got := ExitCode(hard); got != 1 {
		t.Fatalf("hard exit = %d", got)
	}
	ver := Faults{NewFault(KindVerify, CodeConstraintViolation, 0, 0, "pixels")}
	if got := ExitCode(ver); got != 2 {
		t.Fatalf("verify exit = %d", got)
	}
	mixed := append(rec, hard...)
	mixed.Sort()
	if got := ExitCode(mixed); got != 1 {
		t.Fatalf("mixed exit = %d", got)
	}
}

func TestSortKeyOrdering(t *testing.T) {
	a := Frame{Track: 1, Sequence: 2, PTS: 10}
	b := Frame{Track: 2, Sequence: 1, PTS: 10}
	c := Frame{Track: 1, Sequence: 1, PTS: 20}
	if !(SortKey(a)[0] == SortKey(b)[0]) {
		t.Fatal("same timestamp keys differ")
	}
	if !less(SortKey(a), SortKey(b)) || !less(SortKey(a), SortKey(c)) {
		t.Fatal("ordering wrong")
	}
}

func less(a, b [5]int64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
