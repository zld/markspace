package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"archivecheck/internal/manifest"
)

func TestGenerateCleanArchiveShape(t *testing.T) {
	root := t.TempDir()
	res, err := Generate(Config{Seed: 42, Root: root, Fault: FaultNone})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Segments < 12 {
		t.Fatalf("segments=%d want at least 12", m.Segments)
	}
	if len(m.Reps) < 2 {
		t.Fatalf("representations=%d want at least 2", len(m.Reps))
	}
	for _, r := range m.Reps {
		if got := len(m.SegmentsByRep[r.ID]); got != m.Segments {
			t.Fatalf("rep %s has %d segments", r.ID, got)
		}
	}
	// Every recorded file digest must match what is on disk.
	for rel, want := range m.Files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("%s digest %s want %s", rel, got, want)
		}
	}
}

func TestGeneratedManifestsParse(t *testing.T) {
	root := t.TempDir()
	res, err := Generate(Config{Seed: 7, Root: root, Fault: FaultNone})
	if err != nil {
		t.Fatal(err)
	}
	mpd, err := os.Open(filepath.Join(root, res.Manifest.DashFile))
	if err != nil {
		t.Fatal(err)
	}
	defer mpd.Close()
	plan, err := manifest.ParseDASH(res.Manifest.DashFile, mpd, manifest.Options{})
	if err != nil {
		t.Fatalf("MPD: %v", err)
	}
	if len(plan.Representations) != len(res.Manifest.Reps) {
		t.Fatalf("reps=%d", len(plan.Representations))
	}
	if plan.SegmentCount() != res.Manifest.Segments*len(res.Manifest.Reps) {
		t.Fatalf("segments=%d", plan.SegmentCount())
	}

	m3u8, err := os.Open(filepath.Join(root, res.Manifest.HLSFile))
	if err != nil {
		t.Fatal(err)
	}
	defer m3u8.Close()
	hls, err := manifest.ParseHLS(res.Manifest.HLSFile, m3u8, manifest.Options{})
	if err != nil {
		t.Fatalf("HLS: %v", err)
	}
	if hls.SegmentCount() != res.Manifest.Segments {
		t.Fatalf("hls segments=%d", hls.SegmentCount())
	}

	ranges, err := os.Open(filepath.Join(root, res.Manifest.RangeFile))
	if err != nil {
		t.Fatal(err)
	}
	defer ranges.Close()
	rp, err := manifest.ParseHLS(res.Manifest.RangeFile, ranges, manifest.Options{})
	if err != nil {
		t.Fatalf("range playlist: %v", err)
	}
	for _, seg := range rp.Representations[0].Segments {
		if seg.Range == nil {
			t.Fatalf("segment %s lacks a byte range", seg.ID)
		}
	}
}

func TestBurstLossStraddlesBoundary(t *testing.T) {
	root := t.TempDir()
	res, err := Generate(Config{Seed: 42, Root: root, Fault: FaultBurstLoss})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly two adjacent low-layer captures must be short of packets.
	long := 0
	for _, info := range res.Manifest.SegmentsByRep["v-low"] {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(info.Capture)))
		if err != nil {
			t.Fatal(err)
		}
		c, err := DecodeCapture(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Packets) != DefaultFramesPerSeg {
			long++
		}
	}
	if long != 2 {
		t.Fatalf("%d captures lost packets, want 2", long)
	}
	// The same timeline is damaged on both video layers, so a playlist that
	// addresses either layer observes the loss.
	shortHigh := 0
	for _, info := range res.Manifest.SegmentsByRep["v-high"] {
		raw, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(info.Capture)))
		c, _ := DecodeCapture(raw)
		if len(c.Packets) != DefaultFramesPerSeg {
			shortHigh++
		}
	}
	if shortHigh != 2 {
		t.Fatalf("high layer lost packets in %d segments, want 2", shortHigh)
	}
}

func TestDuplicateReorderKeepsEveryPacket(t *testing.T) {
	root := t.TempDir()
	res, err := Generate(Config{Seed: 42, Root: root, Fault: FaultDuplicateReorder})
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range res.Manifest.SegmentsByRep["v-high"] {
		raw, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(info.Capture)))
		c, _ := DecodeCapture(raw)
		seen := map[uint16]bool{}
		for _, p := range c.Packets {
			seen[p.Sequence] = true
		}
		if len(seen) != DefaultFramesPerSeg {
			t.Fatalf("segment %s lost packets: %d distinct", info.ID, len(seen))
		}
		if len(c.Packets) <= DefaultFramesPerSeg {
			t.Fatalf("segment %s has no duplicates", info.ID)
		}
	}
}

func TestTruncatedSegmentIsShorterThanDeclared(t *testing.T) {
	root := t.TempDir()
	res, err := Generate(Config{Seed: 42, Root: root, Fault: FaultTruncatedBox})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := res.Manifest.SegmentInfoFor("v-low", "7")
	if !ok {
		t.Fatal("segment 7 missing")
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(info.URI)))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != info.Size {
		t.Fatalf("size=%d info=%d", len(data), info.Size)
	}
	// The file still starts with a valid styp box (size + type).
	if string(data[4:8]) != "styp" {
		t.Fatalf("unexpected first box type %q", data[4:8])
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	ra, err := Generate(Config{Seed: 99, Root: a, Fault: FaultNone})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := Generate(Config{Seed: 99, Root: b, Fault: FaultNone})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range ra.Manifest.Files {
		if rb.Manifest.Files[k] != v {
			t.Fatalf("file %s differs between runs", k)
		}
	}
}

func TestParseFault(t *testing.T) {
	for _, f := range KnownFaults() {
		got, err := ParseFault(string(f))
		if err != nil || got != f {
			t.Fatalf("ParseFault(%q)=%q,%v", f, got, err)
		}
	}
	if _, err := ParseFault("bogus"); err == nil {
		t.Fatal("bogus fault must fail")
	}
}
