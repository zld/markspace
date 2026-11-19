package program

import (
	"errors"
	"testing"

	"tsinspect/internal/psi"
)

func TestPMTMergesSectionsAndDetectsConflicts(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 0, 1, 0x1000)
	first := section(psi.TableIDPMT, 1, 0, true, 0, 1, pmtBody(0x100, 0x1b, 0x01, 0x01))
	second := section(psi.TableIDPMT, 1, 0, true, 1, 1, pmtBody(0x100, 0x0f, 0x01, 0x02))
	if err := m.ApplyPMT(first, 0x1000); err != nil {
		t.Fatal(err)
	}
	if m.PMT(1) != nil {
		t.Fatal("incomplete PMT became effective")
	}
	if err := m.ApplyPMT(second, 0x1000); err != nil {
		t.Fatal(err)
	}
	if pmt := m.PMT(1); pmt == nil || len(pmt.Streams) != 2 || pmt.PCRPID != 0x100 {
		t.Fatalf("merged PMT wrong: %+v", pmt)
	}

	dup := section(psi.TableIDPMT, 1, 0, true, 1, 1, pmtBody(0x100, 0x02, 0x01, 0x01))
	m2 := NewManager()
	applyPAT(t, m2, 0, 1, 0x1000)
	_ = m2.ApplyPMT(section(psi.TableIDPMT, 1, 0, true, 0, 1, pmtBody(0x100, 0x1b, 0x01, 0x01)), 0x1000)
	if err := m2.ApplyPMT(dup, 0x1000); err == nil {
		t.Fatal("duplicate PID across sections was accepted")
	}
	if m2.PMT(1) != nil {
		t.Fatal("inconsistent PMT became effective")
	}
}

func TestPMTProgramNumberMismatch(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 0, 1, 0x1000)
	err := m.ApplyPMT(section(psi.TableIDPMT, 2, 0, true, 0, 0, pmtBody(0x100)), 0x1000)
	if err == nil {
		t.Fatal("PMT with mismatched program_number accepted")
	}
	if m.PMT(1) != nil {
		t.Fatal("mismatched PMT became effective")
	}
}

func TestPMTSameVersionChangeRejected(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 0, 1, 0x1000)
	if err := m.ApplyPMT(section(psi.TableIDPMT, 1, 0, true, 0, 0, pmtBody(0x100, 0x1b, 0x01, 0x01)), 0x1000); err != nil {
		t.Fatal(err)
	}
	err := m.ApplyPMT(section(psi.TableIDPMT, 1, 0, true, 0, 0, pmtBody(0x100, 0x1b, 0x01, 0x01, 0x0f, 0x01, 0x02)), 0x1000)
	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if len(m.PMT(1).Streams) != 1 {
		t.Fatal("conflicting same-version PMT overwrote effective streams")
	}
}

func TestPMTVersionChangeAndWrapFlag(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 0, 1, 0x1000)
	for v := uint8(0); v < 31; v++ {
		body := pmtBody(0x100, 0x1b, 0x01, byte(0x10+v))
		if err := m.ApplyPMT(section(psi.TableIDPMT, 1, v, true, 0, 0, body), 0x1000); err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
	}
	// 31 -> 0 wraps the 5-bit space.
	if err := m.ApplyPMT(section(psi.TableIDPMT, 1, 0, true, 0, 0, pmtBody(0x100, 0x1b, 0x01, 0x30)), 0x1000); err != nil {
		t.Fatal(err)
	}
	events := m.Events()
	last := events[len(events)-1]
	if last.Status != StatusAppliedVersionWrap {
		t.Fatalf("wrap not flagged: %+v", last)
	}
	if m.PMTVersionChanges() != 31 {
		t.Fatalf("pmt changes = %d, want 31", m.PMTVersionChanges())
	}
}

func TestRetransmittedVersionDoesNotCount(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 0, 1, 0x1000)
	sec := section(psi.TableIDPMT, 1, 7, true, 0, 0, pmtBody(0x100, 0x1b, 0x01, 0x01))
	for i := 0; i < 5; i++ {
		if err := m.ApplyPMT(sec, 0x1000); err != nil {
			t.Fatal(err)
		}
	}
	if m.PMTVersionChanges() != 0 {
		t.Fatalf("retransmissions counted as changes: %d", m.PMTVersionChanges())
	}
	if m.PMT(1).Streams[0].PID != 0x101 {
		t.Fatal("stream mapping lost")
	}
}

func TestStaleVersionsDoNotReplaceEffectiveTables(t *testing.T) {
	m := NewManager()
	applyPAT(t, m, 3, 1, 0x1000)
	applyPAT(t, m, 4, 1, 0x1000)
	if err := m.ApplyPMT(section(psi.TableIDPMT, 1, 10, true, 0, 0, pmtBody(0x100, 0x1b, 0x01, 0x01)), 0x1000); err != nil {
		t.Fatal(err)
	}
	// Version 5 is behind version 10 in the 5-bit space and must be ignored.
	if err := m.ApplyPMT(section(psi.TableIDPMT, 1, 5, true, 0, 0, pmtBody(0x100, 0x0f, 0x01, 0x02)), 0x1000); err != nil {
		t.Fatal(err)
	}
	if m.PMT(1).Version != 10 || len(m.PMT(1).Streams) != 1 {
		t.Fatalf("stale PMT was applied: %+v", m.PMT(1))
	}
	events := m.Events()
	if events[len(events)-1].Status != StatusStaleVersion {
		t.Fatalf("stale version not reported: %+v", events[len(events)-1])
	}
	if m.PMTVersionChanges() != 0 {
		t.Fatalf("stale version counted as change: %d", m.PMTVersionChanges())
	}
}
