package psi

import (
	"errors"
	"testing"
)

func patSection(pid uint16, version uint8, data []byte) *Section {
	return &Section{PID: pid, Data: data, StartPacket: 0, EndPacket: 0}
}

func TestPATCommitsValidVersion(t *testing.T) {
	tracker := NewPATTracker()
	sec := BuildPATSection(42, 0, 0, 0, []PATProgram{{1, 100}, {2, 101}}, 0x1fff)
	pat, changed, err := tracker.Push(patSection(0, 0, sec))
	if err != nil || pat == nil || changed {
		t.Fatalf("first commit: pat=%v changed=%v err=%v", pat, changed, err)
	}
	if pat.TransportStreamID != 42 || pat.Version != 0 || len(pat.Programs) != 2 {
		t.Fatalf("PAT fields wrong: %+v", pat)
	}
	if pat.Programs[0] != (PATProgram{1, 100}) || pat.Programs[1] != (PATProgram{2, 101}) {
		t.Fatalf("program entries wrong: %+v", pat.Programs)
	}
	if pat.NetworkPID != 0x1fff {
		t.Fatalf("network PID should default to 0x1fff, got %d", pat.NetworkPID)
	}
}

func TestPATNetworkPID(t *testing.T) {
	tracker := NewPATTracker()
	sec := BuildPATSection(1, 0, 0, 0, []PATProgram{{1, 100}}, 200)
	pat, _, err := tracker.Push(patSection(0, 0, sec))
	if err != nil || pat.NetworkPID != 200 {
		t.Fatalf("network PID not parsed: %+v err=%v", pat, err)
	}
}

func TestPATVersionChangeAndDuplicates(t *testing.T) {
	tracker := NewPATTracker()
	v0 := BuildPATSection(1, 0, 0, 0, []PATProgram{{1, 100}}, 0x1fff)
	v1 := BuildPATSection(1, 1, 0, 0, []PATProgram{{1, 300}}, 0x1fff)

	if _, changed, _ := tracker.Push(patSection(0, 0, v0)); changed {
		t.Fatal("initial version must not count as a change")
	}
	if _, changed, _ := tracker.Push(patSection(0, 0, v0)); changed {
		t.Fatal("repeated identical version must not count as a change")
	}
	pat, changed, err := tracker.Push(patSection(0, 0, v1))
	if err != nil || !changed || pat == nil || pat.Programs[0].PMTPID != 300 {
		t.Fatalf("version 1 should commit and report change: %+v %v %v", pat, changed, err)
	}
}

func TestPATBadCRCDoesNotOverwrite(t *testing.T) {
	tracker := NewPATTracker()
	v0 := BuildPATSection(1, 0, 0, 0, []PATProgram{{1, 100}}, 0x1fff)
	if _, _, err := tracker.Push(patSection(0, 0, v0)); err != nil {
		t.Fatal(err)
	}
	bad := CorruptCRC(BuildPATSection(1, 1, 0, 0, []PATProgram{{1, 999}}, 0x1fff))
	if _, _, err := tracker.Push(patSection(0, 0, bad)); !errors.Is(err, ErrBadCRC) {
		t.Fatalf("expected ErrBadCRC, got %v", err)
	}
	cur := tracker.Current()
	if cur == nil || cur.Programs[0].PMTPID != 100 {
		t.Fatalf("valid mapping must survive bad CRC, got %+v", cur)
	}
	if tracker.RejectedCRC != 1 {
		t.Fatalf("bad CRC not counted: %d", tracker.RejectedCRC)
	}
}

func TestPATMultiSectionAndOutOfOrder(t *testing.T) {
	tracker := NewPATTracker()
	// Two sections, each with one program; push section 1 before section 0.
	s1 := BuildPATSection(7, 2, 1, 1, []PATProgram{{2, 101}}, 0x1fff)
	if pat, _, err := tracker.Push(patSection(0, 0, s1)); err != nil || pat != nil {
		t.Fatalf("partial version must not commit: pat=%v err=%v", pat, err)
	}
	s0 := BuildPATSection(7, 2, 0, 1, []PATProgram{{1, 100}}, 0x1fff)
	pat, _, err := tracker.Push(patSection(0, 0, s0))
	if err != nil || pat == nil {
		t.Fatalf("table should complete out of order: pat=%v err=%v", pat, err)
	}
	if pat.Version != 2 || len(pat.Programs) != 2 || pat.Programs[1].ProgramNumber != 2 {
		t.Fatalf("merged PAT wrong: %+v", pat)
	}
}

func TestPATRejectsWrongTableID(t *testing.T) {
	tracker := NewPATTracker()
	sec := BuildPATSection(1, 0, 0, 0, nil, 0x1fff)
	sec[0] = 0x02 // PMT table id, then recompute a valid CRC
	crc := crc32MPEG2(sec[:len(sec)-4])
	sec[len(sec)-4], sec[len(sec)-3], sec[len(sec)-2], sec[len(sec)-1] =
		byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc)
	if _, _, err := tracker.Push(patSection(0, 0, sec)); !errors.Is(err, ErrWrongTableID) {
		t.Fatalf("expected ErrWrongTableID, got %v", err)
	}
}

func TestSectionVersionWrapsFiveBit(t *testing.T) {
	for v := uint8(0); v < 32; v++ {
		sec := BuildPATSection(1, v, 0, 0, nil, 0x1fff)
		if got := sectionVersion(sec); got != v {
			t.Fatalf("version %d round-trip got %d", v, got)
		}
	}
}
