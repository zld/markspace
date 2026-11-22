package analysis

import (
	"bytes"
	"errors"
	"testing"

	"tsaudit/internal/fixture"
	"tsaudit/internal/report"
	"tsaudit/internal/transport"
)

func analyzeFixture(t *testing.T) *report.Report {
	t.Helper()
	var buf bytes.Buffer
	if err := fixture.Generate(&buf); err != nil {
		t.Fatal(err)
	}
	rep, err := Analyze(&buf, false)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestStandardFixtureReport(t *testing.T) {
	rep := analyzeFixture(t)

	if rep.Input.PacketsTotal != 14 {
		t.Errorf("packets_total=%d, want 14", rep.Input.PacketsTotal)
	}
	if rep.PAT.VersionChanges != 1 || rep.PAT.CurrentVersion != 1 {
		t.Errorf("pat: %+v", rep.PAT)
	}
	if len(rep.PAT.Programs) != 1 || rep.PAT.Programs[0].PMTPID != 110 {
		t.Errorf("final PAT mapping: %+v", rep.PAT.Programs)
	}
	if len(rep.PMTs) != 1 || rep.PMTs[0].VersionChanges != 1 {
		t.Fatalf("pmts: %+v", rep.PMTs)
	}
	gotPIDs := map[uint16]bool{}
	for _, s := range rep.PMTs[0].Streams {
		gotPIDs[s.PID] = true
	}
	if !gotPIDs[300] || !gotPIDs[257] || len(gotPIDs) != 2 {
		t.Errorf("final streams: %v", gotPIDs)
	}

	// Per-interval counts around the switch.
	if len(rep.Intervals) != 2 {
		t.Fatalf("intervals=%d, want 2", len(rep.Intervals))
	}
	iv0 := rep.Intervals[0]
	if iv0.PATVersion != 0 || iv0.Programs[0].PMTPID != 100 {
		t.Errorf("interval 0: %+v", iv0)
	}
	assertStreamPackets(t, iv0.Programs[0].Streams, 256, 3)
	assertStreamPackets(t, iv0.Programs[0].Streams, 257, 2)
	iv1 := rep.Intervals[1]
	if iv1.PATVersion != 1 || iv1.Programs[0].PMTPID != 110 {
		t.Errorf("interval 1: %+v", iv1)
	}
	assertStreamPackets(t, iv1.Programs[0].Streams, 300, 3)
	assertStreamPackets(t, iv1.Programs[0].Streams, 257, 2)

	// Totals count only received packets: video 6, audio 4.
	totals := map[string]int{}
	for _, tt := range rep.StreamTotals {
		totals[tt.Type] = tt.Packets
	}
	if totals["H.264"] != 6 || totals["AAC"] != 4 {
		t.Errorf("stream_totals: %v", totals)
	}
	// Per-PID elementary stream counts.
	perPID := map[uint16]int{}
	for _, es := range rep.ElementaryStreams {
		perPID[es.PID] = es.Packets
	}
	if perPID[256] != 3 || perPID[300] != 3 || perPID[257] != 4 {
		t.Errorf("elementary_streams: %v", perPID)
	}

	// The CC jump on PID 256 accounts for exactly one missing packet;
	// received counts are not extrapolated.
	if rep.Continuity.MissingPackets != 1 || rep.Continuity.DuplicatePackets != 0 {
		t.Errorf("continuity: %+v", rep.Continuity)
	}
	if rep.Unmapped.Packets != 0 || rep.PSI.CRCErrors != 0 {
		t.Errorf("unmapped=%d crc=%d", rep.Unmapped.Packets, rep.PSI.CRCErrors)
	}
}

func assertStreamPackets(t *testing.T, streams []report.StreamCount, pid uint16, want int) {
	t.Helper()
	for _, s := range streams {
		if s.PID == pid {
			if s.Packets != want {
				t.Errorf("pid %d: %d packets, want %d", pid, s.Packets, want)
			}
			return
		}
	}
	t.Errorf("pid %d not found in streams", pid)
}

// TestRemapEffectivePoint sends an old-mapping packet after the switch:
// it must be reported as unmapped, not merged into the new program.
func TestRemapEffectivePoint(t *testing.T) {
	var buf bytes.Buffer
	seq := [][]byte{
		fixture.PSIPacket(0, 0, fixture.PATSection(0, 1, [2]uint16{1, 100})),
		fixture.PSIPacket(100, 0, fixture.PMTSection(0, 1, 256,
			[2]uint16{0x1B, 256}, [2]uint16{0x0F, 257})),
		fixture.Packet(256, true, 0, make([]byte, 184)),
		fixture.PSIPacket(0, 1, fixture.PATSection(1, 1, [2]uint16{1, 110})),
		fixture.PSIPacket(110, 0, fixture.PMTSection(0, 1, 300,
			[2]uint16{0x1B, 300}, [2]uint16{0x0F, 257})),
		fixture.Packet(300, true, 0, make([]byte, 184)),
		fixture.Packet(256, false, 1, make([]byte, 184)), // stale PID 256
	}
	for _, p := range seq {
		buf.Write(p)
	}
	rep, err := Analyze(&buf, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unmapped.Packets != 1 || len(rep.Unmapped.PIDs) != 1 ||
		rep.Unmapped.PIDs[0].PID != 256 || rep.Unmapped.PIDs[0].Packets != 1 {
		t.Fatalf("unmapped: %+v", rep.Unmapped)
	}
	// History keeps its attribution: interval 0 counted the old PID 256.
	if len(rep.Intervals) != 2 {
		t.Fatalf("intervals=%d", len(rep.Intervals))
	}
	assertStreamPackets(t, rep.Intervals[0].Programs[0].Streams, 256, 1)
	assertStreamPackets(t, rep.Intervals[1].Programs[0].Streams, 300, 1)
}

func TestCorruptInputModes(t *testing.T) {
	var buf bytes.Buffer
	if err := fixture.Generate(&buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	data[3*188] = 0x00 // break the sync byte of packet 3
	if _, err := Analyze(bytes.NewReader(data), false); !errors.Is(err, transport.ErrCorrupt) {
		t.Fatalf("strict mode: want ErrCorrupt, got %v", err)
	}
	rep, err := Analyze(bytes.NewReader(data), true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Input.SyncErrors != 1 || rep.Input.PacketsTotal != 13 {
		t.Errorf("tolerant: %+v", rep.Input)
	}
}
