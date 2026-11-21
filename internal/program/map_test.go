package program

import (
	"testing"

	"github.com/example/tsaudit/internal/psi"
)

func TestApplyPATAndPMTIntervals(t *testing.T) {
	m := New()
	m.ApplyPAT(&psi.PAT{Version: 0, Programs: map[uint16]uint16{1: 4096}})
	if pid, ok := m.PMTPID(1); !ok || pid != 4096 {
		t.Fatalf("PMT PID = %d ok=%v, want 4096", pid, ok)
	}

	m.ApplyPMT(&psi.PMT{
		ProgramNumber: 1, Version: 0, PCRPID: 256,
		Streams: []psi.StreamInfo{
			{StreamType: 0x1b, ElementaryPID: 256},
			{StreamType: 0x0f, ElementaryPID: 257},
		},
	}, 10)
	if !m.IsActive(1, 256) || !m.IsActive(1, 257) {
		t.Fatal("v0 elementary PIDs not active")
	}

	// v1 remaps video 256 -> 258 while audio 257 persists.
	m.ApplyPMT(&psi.PMT{
		ProgramNumber: 1, Version: 1, PCRPID: 258,
		Streams: []psi.StreamInfo{
			{StreamType: 0x1b, ElementaryPID: 258},
			{StreamType: 0x0f, ElementaryPID: 257},
		},
	}, 60)
	if m.IsActive(1, 256) {
		t.Fatal("PID 256 must be inactive after the remap")
	}
	if !m.IsActive(1, 258) {
		t.Fatal("PID 258 must be active after the remap")
	}

	m.CloseIntervals(119)
	records := m.Records()
	// Expect separate intervals: 256(v0), 257(v0), 257(v1), 258(v1).
	if len(records) != 4 {
		t.Fatalf("got %d mapping records, want 4", len(records))
	}
	byPID := map[uint16][]MappingRecord{}
	for _, r := range records {
		byPID[r.ElementaryPID] = append(byPID[r.ElementaryPID], r)
	}
	if len(byPID[257]) != 2 {
		t.Fatalf("PID 257 must keep two version intervals, got %d", len(byPID[257]))
	}
	if byPID[256][0].FirstPacket != 10 || byPID[256][0].LastPacket != 59 {
		t.Fatalf("PID 256 v0 interval wrong: %+v", byPID[256][0])
	}
	if byPID[258][0].FirstPacket != 60 || byPID[258][0].LastPacket != 119 {
		t.Fatalf("PID 258 v1 interval wrong: %+v", byPID[258][0])
	}
}

func TestRecordsAreSorted(t *testing.T) {
	m := New()
	m.ApplyPAT(&psi.PAT{Programs: map[uint16]uint16{2: 4100, 1: 4096}})
	m.ApplyPMT(&psi.PMT{ProgramNumber: 2, Streams: []psi.StreamInfo{{ElementaryPID: 300}}}, 0)
	m.ApplyPMT(&psi.PMT{ProgramNumber: 1, Streams: []psi.StreamInfo{{ElementaryPID: 256}}}, 0)
	records := m.Records()
	if records[0].ProgramNumber != 1 || records[1].ProgramNumber != 2 {
		t.Fatalf("records not program-sorted: %+v", records)
	}
}
