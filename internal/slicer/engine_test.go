package slicer

import (
	"os"
	"path/filepath"
	"testing"

	"streamcheck/internal/ts"
)

// buildStream writes a deterministic TS containing one H.264 video PID whose
// frames alternate keyframe/non-keyframe. Each frame is one PES packet at a
// fixed PTS cadence; section tables are emitted first.
func buildStream(t *testing.T, frames int, ptsStep uint64, keyEvery int) []byte {
	t.Helper()
	pat := buildPATSection()
	pmt := buildPMTSection(videoPID)
	var out []byte
	out = append(out, sectionPackets(ts.PIDPAT, pat)...)
	out = append(out, sectionPackets(pmtPID, pmt)...)
	cc := uint8(0)
	for f := 0; f < frames; f++ {
		key := keyEvery > 0 && f%keyEvery == 0
		nalType := byte(1) // non-IDR slice
		if key {
			nalType = ts.H264NALIDR
		}
		payload := []byte{0x00, 0x00, 0x01, nalType, 0xaa, 0xbb}
		pes := mkVideoPES(uint64(f)*ptsStep, payload)
		out = append(out, pesPackets(videoPID, f == 0, &cc, pes)...)
	}
	return out
}

const videoPID = 0x0100

// pmtPID is a var so its low-byte truncation is a runtime conversion rather
// than a compile-time constant overflow.
var pmtPID uint16 = 0x1000

func TestEngineCutsSegmentsOnKeyframes(t *testing.T) {
	dir := t.TempDir()
	// 30 frames at ~333 ms cadence (~10 s); a keyframe every 9 frames ~= 3s.
	data := buildStream(t, 30, 30_000, 9)
	eng := NewEngine(EngineConfig{
		Window:        WindowConfig{TargetSegment: testTarget()},
		SegmentPrefix: "seg-",
	})
	events, err := Run(eng, NewScannerSource(ts.NewScanner(byteReader(data))), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no segments produced")
	}
	// Every emitted file must exist and be non-empty; no temp files remain.
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if filepath.Ext(en.Name()) == ".tmp" {
			t.Fatalf("temporary file left behind: %s", en.Name())
		}
	}
	for _, ev := range events {
		path := filepath.Join(dir, ev.Record.File)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("segment %s missing: %v", ev.Record.File, err)
		}
		if info.Size() == 0 || ev.Bytes == 0 {
			t.Fatalf("empty segment: %s", ev.Record.File)
		}
	}
	if eng.VideoPID() != videoPID {
		t.Fatalf("video PID auto-detect: %#x", eng.VideoPID())
	}
}

func TestEngineSequenceMonotonic(t *testing.T) {
	dir := t.TempDir()
	data := buildStream(t, 40, 30_000, 9)
	eng := NewEngine(EngineConfig{
		Window:        WindowConfig{TargetSegment: testTarget()},
		SegmentPrefix: "s-",
	})
	events, err := Run(eng, NewScannerSource(ts.NewScanner(byteReader(data))), dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, ev := range events {
		if ev.Record.Sequence != uint64(i) {
			t.Fatalf("sequence gap at %d: %d", i, ev.Record.Sequence)
		}
	}
}
