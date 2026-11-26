package fault

import (
	"errors"
	"testing"

	"replaykit/media/clock"
)

func TestLedgerSortAndSequence(t *testing.T) {
	var l Ledger
	l.Add(New("720p", 100, CategoryPacketLost, SeverityError, ActionSkipped, 90000, Range{clock.Tick{Value: 10}, clock.Tick{Value: 20}}, "b"))
	l.Add(New("360p", 50, CategoryTruncatedBox, SeverityWarning, ActionSkipped, 90000, Range{clock.Tick{Value: 1}, clock.Tick{Value: 2}}, "a"))
	l.Add(New("720p", 100, CategoryPacketDuplicate, SeverityInfo, ActionDropped, 90000, Range{clock.Tick{Value: 5}, clock.Tick{Value: 6}}, "c"))
	l.Add(New("360p", 50, CategoryPacketLost, SeverityError, ActionSkipped, 90000, Range{clock.Tick{Value: 1}, clock.Tick{Value: 2}}, "d"))

	got := l.Anomalies()
	if len(got) != 4 {
		t.Fatalf("len = %d", len(got))
	}
	wantCats := []Category{CategoryPacketLost, CategoryTruncatedBox, CategoryPacketDuplicate, CategoryPacketLost}
	for i, a := range got {
		if a.Sequence != i+1 {
			t.Fatalf("seq[%d] = %d", i, a.Sequence)
		}
		if a.Category != wantCats[i] {
			t.Fatalf("order[%d] = %s, want %s", i, a.Category, wantCats[i])
		}
	}
	// Determinism: sorting twice yields identical output.
	again := l.Anomalies()
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("non-deterministic at %d: %+v vs %+v", i, got[i], again[i])
		}
	}
}

func TestLedgerSeverity(t *testing.T) {
	var l Ledger
	l.Add(New("720p", 0, CategoryTruncatedBox, SeverityWarning, ActionSkipped, 90000, Range{}, ""))
	l.Add(New("720p", 0, CategorySampleOverflow, SeverityError, ActionResynced, 90000, Range{}, ""))
	if l.HighestSeverity() != SeverityError {
		t.Fatalf("highest = %s", l.HighestSeverity())
	}
	if l.CountBySeverity(SeverityError) != 1 {
		t.Fatalf("count error = %d", l.CountBySeverity(SeverityError))
	}
	if l.CountBySeverity(SeverityInfo) != 2 {
		t.Fatalf("count info = %d", l.CountBySeverity(SeverityInfo))
	}
}

func TestFatalAndRecoverable(t *testing.T) {
	r := Range{Start: clock.Tick{Value: 100}, End: clock.Tick{Value: 200}}
	base := errors.New("no mapping")
	fatal := Fatal("map_time", "720p", 4096, r, base)
	if !IsFatal(fatal) {
		t.Fatal("expected fatal")
	}
	if !errors.Is(fatal, base) {
		t.Fatal("fatal must unwrap")
	}
	if got := fatal.Error(); got == "" {
		t.Fatal("empty fatal message")
	}
	if IsFatal(Recoverable(CategoryPacketLost, "audio", 12, r, "gap")) {
		t.Fatal("recoverable must not be fatal")
	}
	re, ok := AsRecoverable(Recoverable(CategoryPacketLost, "audio", 12, r, "gap"))
	if !ok || re.Category != CategoryPacketLost || re.Layer != "audio" {
		t.Fatalf("recoverable = %+v ok=%v", re, ok)
	}
}

func TestRecordRecoverable(t *testing.T) {
	var l Ledger
	l.Record(Recoverable(CategoryPacketLate, "audio", 9, Range{clock.Tick{Value: 1}, clock.Tick{Value: 2}}, "late"))
	l.Record(errors.New("plain error is ignored"))
	got := l.Anomalies()
	if len(got) != 1 || got[0].Category != CategoryPacketLate || got[0].Severity != SeverityWarning {
		t.Fatalf("got %+v", got)
	}
}

func TestRange(t *testing.T) {
	r := Range{Start: clock.Tick{Value: 10}, End: clock.Tick{Value: 40}}
	if r.IsEmpty() || r.Duration() != 30 {
		t.Fatalf("range %+v", r)
	}
	if !(Range{}).IsEmpty() {
		t.Fatal("zero range must be empty")
	}
}
