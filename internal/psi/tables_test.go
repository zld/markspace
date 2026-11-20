package psi

import "testing"

func patSection(version uint8, number, last uint8, entries ...[2]uint16) Section {
	return buildSection(TableIDPAT, 1, version, true, number, last, patBody(entries...))
}

func TestTablesActivateCompletePAT(t *testing.T) {
	tb := NewTables()
	// Two-section PAT: version 0, sections 0 and 1.
	if got, err := tb.AddPAT(patSection(0, 0, 1, [2]uint16{1, 0x100})); err != nil || got != nil {
		t.Fatalf("partial PAT activated early: %v %v", got, err)
	}
	if _, ok := tb.ProgramPMTPID(1); ok {
		t.Fatal("mapping visible before table complete")
	}
	got, err := tb.AddPAT(patSection(0, 1, 1, [2]uint16{2, 0x200}))
	if err != nil || got == nil {
		t.Fatalf("complete PAT not activated: %v %v", got, err)
	}
	if pid, ok := tb.ProgramPMTPID(2); !ok || pid != 0x200 {
		t.Fatalf("program 2 mapping = %d, %v", pid, ok)
	}
	if tb.Stats().PATVersionChanges != 0 {
		t.Fatalf("initial table counted as version change")
	}
}

func TestTablesVersionWrapAndDuplicates(t *testing.T) {
	tb := NewTables()
	if _, err := tb.AddPAT(patSection(31, 0, 0, [2]uint16{1, 0x100})); err != nil {
		t.Fatal(err)
	}
	// Duplicate of the active table.
	if got, err := tb.AddPAT(patSection(31, 0, 0, [2]uint16{1, 0x100})); err != nil || got != nil {
		t.Fatalf("duplicate returned %v %v", got, err)
	}
	if tb.Stats().PATDuplicates != 1 {
		t.Fatalf("duplicates = %d", tb.Stats().PATDuplicates)
	}
	// Version wraps 31 -> 0.
	if _, err := tb.AddPAT(patSection(0, 0, 0, [2]uint16{1, 0x111}, [2]uint16{3, 0x300})); err != nil {
		t.Fatal(err)
	}
	if tb.Stats().PATVersionChanges != 1 {
		t.Fatalf("version changes = %d", tb.Stats().PATVersionChanges)
	}
	if pid, ok := tb.ProgramPMTPID(3); !ok || pid != 0x300 {
		t.Fatalf("program 3 mapping = %d, %v", pid, ok)
	}
}

func TestTablesIncompleteNewVersionKeepsOldMapping(t *testing.T) {
	tb := NewTables()
	if _, err := tb.AddPAT(patSection(0, 0, 0, [2]uint16{1, 0x100})); err != nil {
		t.Fatal(err)
	}
	// Version 1 needs two sections; only the first arrives.
	if _, err := tb.AddPAT(patSection(1, 0, 1, [2]uint16{1, 0x100})); err != nil {
		t.Fatal(err)
	}
	if pid, ok := tb.ProgramPMTPID(1); !ok || pid != 0x100 {
		t.Fatalf("old mapping lost after partial update: %d %v", pid, ok)
	}
	// Packet loss caused this PID's pending table to be dropped.
	tb.DiscardPendingPID(0)
	if got, err := tb.AddPAT(patSection(1, 1, 1, [2]uint16{1, 0x100}, [2]uint16{9, 0x900})); err != nil || got != nil {
		t.Fatalf("discarded version completed: %v %v", got, err)
	}
	if _, ok := tb.ProgramPMTPID(9); ok {
		t.Fatal("stale pending section completed a discarded table")
	}
}

func TestTablesFutureSectionsIgnored(t *testing.T) {
	tb := NewTables()
	future := buildSection(TableIDPAT, 1, 0, false, 0, 0, patBody([2]uint16{1, 0x100}))
	if got, err := tb.AddPAT(future); err != nil || got != nil {
		t.Fatalf("future table activated: %v %v", got, err)
	}
	if tb.Stats().FutureSections != 1 {
		t.Fatalf("future sections = %d", tb.Stats().FutureSections)
	}
}
