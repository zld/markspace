package fixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediasync/internal/manifest"
)

func testOpts(root string) Options {
	return Options{
		Seed: 23, Segments: 6, FramesPerSeg: 16,
		ChunkRecords: 256, Root: filepath.Join(root, "work"),
	}
}

func TestGenerateHealthyAndDamaged(t *testing.T) {
	root := t.TempDir()
	b, err := Generate(root, testOpts(root))
	if err != nil {
		t.Fatal(err)
	}
	// Five fault classes must be present in the generated bundle.
	if len(b.TruncatedSegs) < 1 {
		t.Fatal("truncated segment fault missing")
	}
	burst, total := 0, 0
	for _, p := range b.Packets {
		total++
		if p.Missing {
			burst++
		}
	}
	if burst < 4 || total == 0 {
		t.Fatalf("RTP burst loss missing: burst=%d total=%d", burst, total)
	}
	if len(b.Anchors) < 2 {
		t.Fatalf("drift anchors missing: %+v", b.Anchors)
	}
	// 32-bit timestamp wrap must actually occur on the video tracks.
	if !b.hasTimestampWrap() {
		t.Fatal("no 32-bit timestamp wrap generated")
	}
	// Distinct VFR durations -> at least three interval shifts downstream.
	if got := b.countVFRIntervals(); got < 3 {
		t.Fatalf("vfr intervals = %d, want >=3", got)
	}
}

func (b *Bundle) hasTimestampWrap() bool {
	var prev uint32
	have := false
	for _, s := range b.Samples {
		if s.Representation != RepVideoHi {
			continue
		}
		if have && s.RawTS < prev {
			return true
		}
		prev, have = s.RawTS, true
	}
	return false
}

// countVFRIntervals counts distinct consecutive frame gaps on the video
// track; the fixture cycles four durations so all four must appear.
func (b *Bundle) countVFRIntervals() int {
	gaps := map[uint32]bool{}
	var prev uint32
	have := false
	for _, s := range b.Samples {
		if s.Representation != RepVideoHi {
			continue
		}
		if have {
			gaps[s.RawTS-prev] = true
		}
		prev, have = s.RawTS, true
	}
	return len(gaps)
}

func TestGeneratedMP4ParsesWithGoMp4(t *testing.T) {
	root := t.TempDir()
	b, err := Generate(root, testOpts(root))
	if err != nil {
		t.Fatal(err)
	}
	initPath := filepath.Join(b.Root, RepVideoHi, "init.mp4")
	init, err := ParseInitSegment(mustRead(t, initPath))
	if err != nil {
		t.Fatal(err)
	}
	if init.Timescale != videoTS {
		t.Fatalf("init timescale = %d, want %d", init.Timescale, videoTS)
	}
	segPath := filepath.Join(b.Root, RepVideoHi, "seg-1.m4s")
	seg, err := ParseMediaSegment(mustRead(t, segPath))
	if err != nil {
		t.Fatal(err)
	}
	if seg.SequenceNumber != 1 || seg.SampleCount != 16 || seg.MdatSize == 0 {
		t.Fatalf("media segment parse wrong: %+v", seg)
	}
}

func TestGeneratedManifestsParse(t *testing.T) {
	root := t.TempDir()
	b, err := Generate(root, testOpts(root))
	if err != nil {
		t.Fatal(err)
	}
	tl, inits, err := manifest.ParseDASHBytes(b.MPD)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Events) == 0 || len(inits) != 3 {
		t.Fatalf("DASH parse wrong: events=%d inits=%d",
			len(tl.Events), len(inits))
	}
	hls, err := manifest.ParseHLS(strings.NewReader(string(b.HLS)), videoTS)
	if err != nil {
		t.Fatal(err)
	}
	discos, isolated := 0, 0
	for _, ev := range hls.Events {
		if ev.Discontinuity {
			discos++
		}
		if ev.Isolated {
			isolated++
		}
	}
	if discos < 2 || isolated != discos {
		t.Fatalf("HLS discontinuities=%d isolated=%d", discos, isolated)
	}
}

func TestGenerateDeterministicBySeed(t *testing.T) {
	root := t.TempDir()
	build := func() *Bundle {
		b, err := Generate(root, testOpts(root))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, c := build(), build()
	if len(a.Samples) != len(c.Samples) || len(a.Packets) != len(c.Packets) {
		t.Fatal("seeded generation changed cardinality")
	}
	for i := range a.Samples {
		if a.Samples[i].RawTS != c.Samples[i].RawTS ||
			a.Samples[i].TrueNS != c.Samples[i].TrueNS {
			t.Fatalf("nondeterministic sample at %d", i)
		}
	}
}

func TestPathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	opts := testOpts(root)
	opts.Root = filepath.Join(root, "..", "escape")
	if _, err := Generate(root, opts); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
