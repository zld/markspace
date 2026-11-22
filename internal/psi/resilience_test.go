package psi

import (
	"errors"
	"testing"

	"tsrebuild/internal/tsreader"
)

// A TEI-corrupt continuation must not prevent a later, complete table from
// recovering and committing.
func TestRecoveryAfterTEIThenCompleteTable(t *testing.T) {
	section := BuildPATSection(1, 0, 0, 0, []PATProgram{{ProgramNumber: 1, PMTPID: 100}}, 0x1fff)
	pkts := splitPayload(0, section, 8)
	a := NewAssembler()
	tr := NewPATTracker()

	if _, err := a.Push(pkts[0]); err != nil {
		t.Fatal(err)
	}
	bad := &tsreader.Packet{PID: 0, Index: 1, HasPayload: true, TransportError: true}
	if _, err := a.Push(bad); err != nil {
		t.Fatal(err)
	}

	// A fresh PUSI carrying the same valid section must fully recover.
	var committed bool
	for _, p := range splitPayload(0, section, 8) {
		secs, err := a.Push(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secs {
			pat, _, perr := tr.Push(s)
			if perr != nil {
				t.Fatalf("recovery commit failed: %v", perr)
			}
			if pat != nil {
				committed = true
			}
		}
	}
	if !committed {
		t.Fatal("complete PSI after packet loss did not recover")
	}
}

// A bad-CRC update must leave the active mapping on the prior good version.
func TestBadCRCLeavesActiveMappingIntact(t *testing.T) {
	tr := NewPATTracker()
	good := BuildPATSection(1, 5, 0, 0, []PATProgram{{ProgramNumber: 1, PMTPID: 100}}, 0x1fff)
	if _, _, err := tr.Push(patSection(0, 0, good)); err != nil {
		t.Fatal(err)
	}
	bad := CorruptCRC(BuildPATSection(1, 6, 0, 0, []PATProgram{{ProgramNumber: 1, PMTPID: 200}}, 0x1fff))
	if _, _, err := tr.Push(patSection(0, 0, bad)); !errors.Is(err, ErrBadCRC) {
		t.Fatalf("expected ErrBadCRC, got %v", err)
	}
	cur := tr.Current()
	if cur.Version != 5 || cur.Programs[0].PMTPID != 100 {
		t.Fatalf("active mapping changed after bad CRC: %+v", cur)
	}
}
