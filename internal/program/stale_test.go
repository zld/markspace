package program

import (
	"testing"

	"tsinspect/internal/psi"
)

func TestStalePATIgnored(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 20, 1, 0x1000)
	applyPAT(t, m, 21, 1, 0x1000, 2, 0x2000)
	applyPAT(t, m, 10, 1, 0x1000) // stale: modularly behind 21
	if len(m.PAT().Programs) != 2 {
		t.Fatalf("stale PAT replaced effective table: %+v", m.PAT())
	}
	if m.PATVersionChanges() != 1 {
		t.Fatalf("PAT changes = %d, want 1", m.PATVersionChanges())
	}
}

func TestVersionWrapIsApplied(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 30, 1, 0x1000)
	applyPAT(t, m, 31, 1, 0x1000)
	applyPAT(t, m, 0, 1, 0x1000, 2, 0x2000)
	if m.PAT().Version != 0 || len(m.PAT().Programs) != 2 {
		t.Fatalf("wrap not applied: %+v", m.PAT())
	}
	last := m.Events()[len(m.Events())-1]
	if last.Status != StatusAppliedVersionWrap {
		t.Fatalf("wrap status = %s", last.Status)
	}
}

func TestPATWithSharedPMTPIDIsRejected(t *testing.T) {
	m := NewManager()
	err := m.ApplyPAT(section(psi.TableIDPAT, 1, 0, true, 0, 0, patBody(1, 0x1000, 2, 0x1000)))
	if err == nil {
		t.Fatal("PAT assigning one PMT PID to two programs was accepted")
	}
	if m.PAT() != nil {
		t.Fatal("invalid PAT became effective")
	}
}
