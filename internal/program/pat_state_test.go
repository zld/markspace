package program

import (
	"errors"
	"testing"

	"tsinspect/internal/psi"
)

func TestPATVersionChangeTakesEffect(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 0, 1, 0x100, 2, 0x200)
	if m.PAT().Version != 0 || m.PATVersionChanges() != 0 {
		t.Fatalf("initial PAT should not count as change: %+v", m.PAT())
	}
	applyPAT(t, m, 1, 1, 0x100, 2, 0x200, 3, 0x300)
	if m.PAT().Version != 1 || m.PATVersionChanges() != 1 {
		t.Fatalf("version change not applied: changes=%d", m.PATVersionChanges())
	}
	if len(m.PAT().Programs) != 3 {
		t.Fatalf("programs = %d, want 3", len(m.PAT().Programs))
	}
}

func TestPATSameVersionConflictKeepsOldTable(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 2, 1, 0x100)
	err := m.ApplyPAT(section(psi.TableIDPAT, 1, 2, true, 0, 0, patBody(1, 0x150)))
	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if m.PAT().Programs[1] != 0x100 {
		t.Fatal("conflicting same-version PAT overwrote the effective table")
	}
}

func TestPATCurrentNextFalseIgnored(t *testing.T) {
	m := NewManager()
	if err := m.ApplyPAT(section(psi.TableIDPAT, 1, 4, false, 0, 0, patBody(1, 0x100))); err != nil {
		t.Fatal(err)
	}
	if m.PAT() != nil {
		t.Fatal("current_next=0 PAT became effective")
	}
	events := m.Events()
	if len(events) != 1 || events[0].Status != StatusIgnoredNextTable {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestPATMultiSectionRequiresAllSections(t *testing.T) {
	m := NewManager()
	if err := m.ApplyPAT(section(psi.TableIDPAT, 1, 0, true, 0, 1, patBody(1, 0x100))); err != nil {
		t.Fatal(err)
	}
	if m.PAT() != nil {
		t.Fatal("incomplete multi-section PAT became effective")
	}
	if err := m.ApplyPAT(section(psi.TableIDPAT, 1, 0, true, 1, 1, patBody(2, 0x200))); err != nil {
		t.Fatal(err)
	}
	if m.PAT() == nil || len(m.PAT().Programs) != 2 {
		t.Fatalf("merged PAT wrong: %+v", m.PAT())
	}
}

func TestPATRemovalDropsPMTState(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 0, 1, 0x100, 2, 0x200)
	if err := m.ApplyPMT(section(psi.TableIDPMT, 2, 0, true, 0, 0, pmtBody(0x200, 0x02, 0x02, 0x00)), 0x200); err != nil {
		t.Fatal(err)
	}
	if m.PMT(2) == nil {
		t.Fatal("PMT not applied")
	}
	applyPAT(t, m, 1, 1, 0x100)
	if m.PMT(2) != nil {
		t.Fatal("removed program kept its PMT")
	}
	found := false
	for _, ev := range m.Events() {
		if ev.Kind == "pmt" && ev.Status == StatusRemoved && ev.ProgramNumber == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no removal event: %+v", m.Events())
	}
}
