package model

import "testing"

func TestRatExactArithmetic(t *testing.T) {
	third := MustRat(1, 3)
	sum := third.Add(third).Add(third)
	if !sum.Equal(RatFromInt(1)) {
		t.Fatalf("third*3 = %s, want 1", sum)
	}
	if third.Add(MustRat(1, 6)).String() != "1/2" {
		t.Fatalf("canonical reduction failed: %s", third.Add(MustRat(1, 6)))
	}
	if _, err := NewRat(1, 0); err == nil {
		t.Fatal("zero denominator must fail")
	}
	if _, err := third.Quo(RatFromInt(0)); err == nil {
		t.Fatal("division by zero must fail")
	}
}

func TestRatTextRoundTrip(t *testing.T) {
	cases := []string{"0", "1", "-3", "1001/30000", "1/2"}
	for _, c := range cases {
		r, err := ParseRat(c)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		b, err := r.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back Rat
		if err := back.UnmarshalText(b); err != nil {
			t.Fatal(err)
		}
		if !back.Equal(r) || back.String() != c {
			t.Fatalf("roundtrip %s -> %s", c, back)
		}
	}
	if _, err := ParseRat("1/0"); err == nil {
		t.Fatal("1/0 must be rejected")
	}
}

func TestSpanContainsHalfOpen(t *testing.T) {
	s := Span{Start: RatFromInt(0), End: RatFromInt(10)}
	if !s.Contains(RatFromInt(0)) || s.Contains(RatFromInt(10)) {
		t.Fatal("span must be half-open")
	}
}

func TestErrorClassification(t *testing.T) {
	e := NewError(CodeInvalidFrameNumber, "timecode", "tc", InputPos{File: "a.srt", Line: 3, Column: 7, Offset: 42},
		"frame 31 out of range")
	if !e.Recoverable() {
		t.Fatal("bad frame number must be recoverable per-segment")
	}
	if got := e.Error(); got == "" || e.Pos.String() != "a.srt:3:7@42" {
		t.Fatalf("position not retained: %q / %q", got, e.Pos.String())
	}
	fatal := NewError(CodeCheckpointMismatch, "pipeline", "", InputPos{}, "digest changed")
	if !NewError(CodeIndexCorrupt, "mp4", "", InputPos{}, "bad").Code.Fatal() {
		t.Fatal("an unreadable index must be fatal for that input")
	}
	if fatal.Recoverable() || !fatal.Code.Fatal() {
		t.Fatal("checkpoint mismatch must be fatal")
	}
	var es Errors
	es.Add(e)
	es.Add(NewWarning(CodeIndexFallback, "mp4", "v", InputPos{}, "scan"))
	es.Add(fatal)
	if es.RecoverableCount() != 1 || es.WarningCount() != 1 || es.FirstFatal() != fatal {
		t.Fatalf("classification wrong: %#v", es)
	}
}
