package video

import (
	"strings"
	"testing"

	"mediatool/internal/fault"
)

func cand(src string, pri int, p, t, m uint16, off int64) Candidate {
	return Candidate{Source: src, Priority: pri, Color: Color{Primaries: p, Transfer: t, Matrix: m, Known: true}, Offset: off}
}

func TestHighestPriorityWinsAndConflictsRecorded(t *testing.T) {
	dec, err := Normalize([]Candidate{
		cand("sidecar:cam-b", 2, 9, 16, 9, 20),
		cand("container", 0, 1, 1, 1, 10),
		cand("sidecar:cam-a", 1, 1, 1, 1, 15),
	})
	if err != nil {
		// A conflict is reported as a warning fault; it must not abort.
		if f, ok := fault.As(err); !ok || f.Severity != fault.SeverityWarning {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if dec.Chosen.Primaries != 1 || dec.Chosen.Source != "container" || dec.Chosen.SourceRank != 0 {
		t.Fatalf("winner wrong: %+v", dec.Chosen)
	}
	if len(dec.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1 (the BT.2020 sidecar)", len(dec.Conflicts))
	}
	if dec.Conflicts[0].Source != "sidecar:cam-b" || dec.Conflicts[0].Offset != 20 {
		t.Fatalf("conflict provenance wrong: %+v", dec.Conflicts[0])
	}
}

func TestMissingMetadataIsUnknown(t *testing.T) {
	dec, err := Normalize(nil)
	if err != nil {
		t.Fatalf("Normalize(nil): %v", err)
	}
	if dec.Chosen.Known {
		t.Fatalf("missing metadata must be unknown, got %+v", dec.Chosen)
	}
	if !strings.Contains(dec.Record(), "unknown") {
		t.Fatalf("record should say unknown: %s", dec.Record())
	}
}

func TestUnreadableCandidatesAreIgnored(t *testing.T) {
	dec, err := Normalize([]Candidate{
		{Source: "probe", Priority: -1, Color: Color{Primaries: 4, Transfer: 4, Matrix: 4, Known: true}},
		cand("container", 3, 1, 1, 1, 1),
	})
	if err != nil {
		t.Fatalf("negative priority should be skipped, not conflicted: %v", err)
	}
	if dec.Chosen.Primaries != 1 {
		t.Fatalf("winner = %+v, want the container", dec.Chosen)
	}
}

func TestDeterministicTieBreak(t *testing.T) {
	a, _ := Normalize([]Candidate{
		cand("zeta", 1, 9, 16, 9, 1),
		cand("alpha", 1, 1, 1, 1, 2),
	})
	b, _ := Normalize([]Candidate{
		cand("alpha", 1, 1, 1, 1, 2),
		cand("zeta", 1, 9, 16, 9, 1),
	})
	if a.Chosen.Key() != b.Chosen.Key() || a.Chosen.Source != b.Chosen.Source {
		t.Fatalf("tie-break not deterministic: %+v vs %+v", a.Chosen, b.Chosen)
	}
}

func TestRecordCarriesSourceAndConflicts(t *testing.T) {
	dec, _ := Normalize([]Candidate{
		cand("container", 0, 1, 1, 1, 1),
		cand("sidecar", 1, 9, 16, 9, 2),
	})
	r := dec.Record()
	for _, want := range []string{"source=container", "rank=0", "conflicts=sidecar:"} {
		if !strings.Contains(r, want) {
			t.Fatalf("record %q missing %q", r, want)
		}
	}
}
