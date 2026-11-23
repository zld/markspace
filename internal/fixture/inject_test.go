package fixture_test

import (
	"testing"

	"mediacore/internal/fixture"
)

func TestInjectionIsDeterministic(t *testing.T) {
	for _, kind := range fixture.AllFaults() {
		a, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{kind})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		b, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{kind})
		if err != nil {
			t.Fatal(err)
		}
		if a.Digest != b.Digest {
			t.Fatalf("%s: digest differs between identical runs", kind)
		}
		if len(a.Injections) != 1 || len(b.Injections) != 1 {
			t.Fatalf("%s: injections=%d", kind, len(a.Injections))
		}
		if a.Injections[0].ByteOffset != b.Injections[0].ByteOffset {
			t.Fatalf("%s: offset differs", kind)
		}
	}
}

func TestInjectionOffsetsAreInsideFinalStream(t *testing.T) {
	// Applying faults in sequence rebuilds the stream, so a stale offset would
	// point at the wrong byte or outside the file.
	kinds := []fixture.FaultKind{
		fixture.FaultTfdtRegression,
		fixture.FaultDuplicate,
		fixture.FaultIndexGap,
		fixture.FaultSequenceGap,
		fixture.FaultCorruptMdat,
		fixture.FaultOversizeBox,
		fixture.FaultTruncation,
	}
	for _, kind := range kinds {
		s, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{kind})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		inj := s.Injections[0]
		if inj.ByteOffset < 0 || inj.ByteOffset > int64(len(s.Data)) {
			t.Fatalf("%s: offset %d outside %d byte stream", kind, inj.ByteOffset, len(s.Data))
		}
	}
}

func TestCombinedFaultsKeepOffsetsResolved(t *testing.T) {
	kinds := []fixture.FaultKind{fixture.FaultTfdtRegression, fixture.FaultIndexGap, fixture.FaultTruncation}
	s, err := fixture.Inject(41, 2, 24, kinds)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Injections) != len(kinds) {
		t.Fatalf("injections=%d want %d", len(s.Injections), len(kinds))
	}
	truncation := s.Injections[len(s.Injections)-1]
	if truncation.Kind != fixture.FaultTruncation {
		t.Fatalf("truncation must be applied last, got %s", truncation.Kind)
	}
	// A truncation is anchored at the moof header it cut, which must lie in
	// the surviving prefix and be the box a parser reports.
	if truncation.ByteOffset >= int64(len(s.Data)) {
		t.Fatalf("truncation offset %d not inside surviving %d bytes", truncation.ByteOffset, len(s.Data))
	}
	if s.TruncatedTo != int64(len(s.Data)) {
		t.Fatalf("truncated_to %d != final size %d", s.TruncatedTo, len(s.Data))
	}
	for _, inj := range s.Injections[:len(s.Injections)-1] {
		if inj.ByteOffset >= int64(len(s.Data)) {
			t.Fatalf("%s offset %d is past the truncated file end %d", inj.Kind, inj.ByteOffset, len(s.Data))
		}
	}
	// Every non-truncation fault must still point at its own field.
	for _, inj := range s.Injections {
		if inj.Anchor() == 0 && inj.Kind != fixture.FaultTruncation {
			t.Fatalf("%s has no resolved anchor", inj.Kind)
		}
	}
}

func TestParseFaultsRejectsUnknown(t *testing.T) {
	if _, err := fixture.ParseFaults([]string{"truncation", "bogus"}); err == nil {
		t.Fatal("unknown fault accepted")
	}
	got, err := fixture.ParseFaults([]string{"truncation,tfdt-regression", "index-gap"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d faults", len(got))
	}
}
