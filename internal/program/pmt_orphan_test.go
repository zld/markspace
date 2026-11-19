package program

import (
	"testing"

	"tsinspect/internal/psi"
)

func TestPMTOrphanUntilPATArrives(t *testing.T) {
	m := NewManager()
	pmtSec := section(psi.TableIDPMT, 1, 3, true, 0, 0, pmtBody(0x100, 0x1b, 0x01, 0x01))
	if err := m.ApplyPMT(pmtSec, 0x1000); err != nil {
		t.Fatal(err)
	}
	if m.PMT(1) != nil {
		t.Fatal("orphan PMT became effective")
	}
	if m.OrphanPMTs() != 1 {
		t.Fatalf("orphan count = %d", m.OrphanPMTs())
	}
	applyPAT(t, m, 0, 1, 0x1000)
	m.AdoptUnboundPMTs()
	if m.PMT(1) == nil || len(m.PMT(1).Streams) != 1 {
		t.Fatalf("orphan PMT not adopted after PAT: %+v", m.PMT(1))
	}
}
