// Package integration_test exercises the full pipeline: fixture generation,
// analysis, slicing, slice re-reads and malformed input handling.
package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"tsmap/internal/demux"
	"tsmap/internal/fixture"
	"tsmap/internal/slicer"
	"tsmap/internal/ts"
)

func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "demo.ts")
	var buf bytes.Buffer
	if err := fixture.Generate(&buf, fixture.ProfileVersionsAndGap); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func esByPID(s *demux.Summary, pid uint16) *demux.ESStats {
	for i := range s.ES {
		if s.ES[i].PID == pid {
			return &s.ES[i]
		}
	}
	return nil
}

func TestAnalyzeFixture(t *testing.T) {
	s, err := demux.AnalyzeFile(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.Fatal != "" {
		t.Fatalf("unexpected fatal: %s", s.Fatal)
	}
	if s.PATVersionChanges != 1 {
		t.Errorf("pat_version_changes = %d, want 1", s.PATVersionChanges)
	}
	if s.PMTVersionChanges[1] != 1 {
		t.Errorf("pmt_version_changes[1] = %d, want 1", s.PMTVersionChanges[1])
	}
	if s.PMTVersionChanges[2] != 0 {
		t.Errorf("pmt_version_changes[2] = %d, want 0", s.PMTVersionChanges[2])
	}
	if len(s.Programs) != 2 {
		t.Fatalf("programs = %d, want 2", len(s.Programs))
	}
	p1, p2 := s.Programs[0], s.Programs[1]
	if p1.Number != 1 || p1.PMTPID != 256 || p1.PCRPID != 257 {
		t.Errorf("program 1 mapping wrong: %+v", p1)
	}
	if len(p1.Streams) != 2 || p1.Streams[0].PID != 257 || p1.Streams[0].StreamType != 0x1b ||
		p1.Streams[1].PID != 258 || p1.Streams[1].StreamType != 0x0f {
		t.Errorf("program 1 streams wrong: %+v", p1.Streams)
	}
	if p2.Number != 2 || p2.PMTPID != 272 || len(p2.Streams) != 1 ||
		p2.Streams[0].PID != 513 || p2.Streams[0].StreamType != 0x81 {
		t.Errorf("program 2 mapping wrong (unknown stream_type must survive): %+v", p2)
	}
	video := esByPID(s, 257)
	if video == nil || video.PayloadPackets != 5 || video.GapEvents != 1 || video.EstimatedLost != 1 {
		t.Errorf("video stats wrong: %+v", video)
	}
	if a := esByPID(s, 258); a == nil || a.PayloadPackets != 4 {
		t.Errorf("audio stats wrong: %+v", a)
	}
	if x := esByPID(s, 513); x == nil || x.PayloadPackets != 3 {
		t.Errorf("prog2 stream stats wrong: %+v", x)
	}
	if video.FirstPTS == nil || *video.FirstPTS != 90000 {
		t.Errorf("video first PTS wrong: %v", video.FirstPTS)
	}
}

func TestSliceAndReanalyze(t *testing.T) {
	src := writeFixture(t)
	outDir := t.TempDir()
	res, err := slicer.Slice(src, slicer.Options{OutDir: outDir, MaxPackets: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 3 {
		t.Fatalf("slices = %d, want 3", len(res.Files))
	}
	for _, f := range res.Files {
		if f.InjectedPAT != 1 {
			t.Errorf("%s: injected PAT = %d, want 1", f.Path, f.InjectedPAT)
		}
	}
	// Every slice head must carry parseable, complete program tables.
	for _, f := range res.Files {
		s, err := demux.AnalyzeFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !s.HasCompleteTables {
			t.Errorf("%s: no complete tables after re-read", f.Path)
			continue
		}
		if len(s.Programs) != 2 || s.Programs[0].PMTPID != 256 || s.Programs[1].PMTPID != 272 {
			t.Errorf("%s: program mapping wrong after re-read: %+v", f.Path, s.Programs)
		}
	}
}

func TestTruncatedInput(t *testing.T) {
	src := writeFixture(t)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	trunc := filepath.Join(t.TempDir(), "trunc.ts")
	if err := os.WriteFile(trunc, data[:len(data)-100], 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := demux.AnalyzeFile(trunc)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range s.Anomalies {
		if a.Kind == "trailing_bytes" {
			found = true
		}
	}
	if !found {
		t.Error("truncated input: trailing_bytes anomaly missing")
	}
	if !s.HasCompleteTables {
		t.Error("truncated input: tables committed before the cut should survive")
	}
}

func rawPacket(pid uint16, cc byte) []byte {
	pkt := make([]byte, ts.PacketSize)
	pkt[0] = ts.SyncByte
	pkt[1] = byte(pid >> 8)
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | cc
	for i := 4; i < len(pkt); i++ {
		pkt[i] = 0xFF
	}
	return pkt
}

func TestNoProgramTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notables.ts")
	var buf bytes.Buffer
	for i := 0; i < 5; i++ {
		buf.Write(rawPacket(300, byte(i)))
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := demux.AnalyzeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Fatal != demux.FatalNoTables {
		t.Errorf("fatal = %q, want %q", s.Fatal, demux.FatalNoTables)
	}
}

func TestUnsyncableInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.ts")
	garbage := make([]byte, 1024)
	for i := range garbage {
		garbage[i] = byte(i*37 + 11) // deterministic, no 0x47 sync runs
	}
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := demux.AnalyzeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Fatal != demux.FatalResync {
		t.Errorf("fatal = %q, want %q", s.Fatal, demux.FatalResync)
	}
}

func TestUnsupportedPacketSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ts192.ts")
	var buf bytes.Buffer
	for i := 0; i < 4; i++ {
		pkt := make([]byte, 192)
		pkt[0] = ts.SyncByte
		buf.Write(pkt)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := demux.AnalyzeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Fatal != demux.FatalResync {
		t.Errorf("fatal = %q, want %q", s.Fatal, demux.FatalResync)
	}
	found := false
	for _, a := range s.Anomalies {
		if a.Kind == "fatal" && bytes.Contains([]byte(a.Detail), []byte("unsupported")) {
			found = true
		}
	}
	if !found {
		t.Error("192-byte input: unsupported packet size not reported")
	}
}
