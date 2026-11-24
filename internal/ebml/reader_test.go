package ebml_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/ebml"
	"mediarun/internal/testkit"
)

type collector struct {
	scale  uint64
	tracks []ebml.Track
	blocks []ebml.Block
	errs   []*ebml.Error
}

func (c *collector) OnInfo(s uint64)        { c.scale = s }
func (c *collector) OnTracks(t []ebml.Track) { c.tracks = t }
func (c *collector) OnBlock(b ebml.Block)   { c.blocks = append(c.blocks, b) }
func (c *collector) OnError(e *ebml.Error)  { c.errs = append(c.errs, e) }

func parseSample(t *testing.T) *collector {
	t.Helper()
	dir := t.TempDir()
	in, err := testkit.Generate(dir, 7, 0.02)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(in.MediaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	c := &collector{}
	if err := ebml.NewParser(f, fi.Size()).Parse(c); err != nil {
		t.Fatalf("fatal parse: %v", err)
	}
	return c
}

func TestParseGeneratedSample(t *testing.T) {
	c := parseSample(t)
	if c.scale != 1000000 {
		t.Errorf("scale=%d, want 1000000", c.scale)
	}
	if len(c.tracks) != 2 {
		t.Fatalf("tracks=%d, want 2", len(c.tracks))
	}
	if c.tracks[1].Rate != 44100 || c.tracks[1].Channels != 1 {
		t.Errorf("audio track %+v", c.tracks[1])
	}
	if len(c.blocks) == 0 {
		t.Fatal("no blocks parsed")
	}
	codes := map[ebml.Code]int{}
	for _, e := range c.errs {
		codes[e.Code]++
	}
	for _, want := range []ebml.Code{
		ebml.CodeCorruptBlock, ebml.CodeMissingTimestamp, ebml.CodeTruncatedElement,
	} {
		if codes[want] == 0 {
			t.Errorf("missing expected error code %s (got %v)", want, codes)
		}
	}
	t.Logf("blocks=%d errors=%v", len(c.blocks), codes)
}

func TestClockWrapCorrection(t *testing.T) {
	c := parseSample(t)
	clk := ebml.NewClock(c.scale)
	var wrap int
	for i := range c.blocks {
		clk.Abs(&c.blocks[i])
	}
	for _, e := range clk.Errors() {
		if e.Code == ebml.CodeTimestampWrap {
			wrap++
		}
	}
	if wrap == 0 {
		t.Error("expected timestamp wrap correction for rel=-30000 block")
	}
	// 修正后各轨时间戳单调
	clk2 := ebml.NewClock(c.scale)
	last := map[uint64]int64{}
	for i := range c.blocks {
		abs := clk2.Abs(&c.blocks[i])
		if prev, ok := last[c.blocks[i].Track]; ok && abs < prev {
			t.Errorf("track %d not monotonic after correction", c.blocks[i].Track)
		}
		last[c.blocks[i].Track] = abs
	}
}

func TestBadHeaderFatal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.mkv")
	if err := os.WriteFile(p, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	err := ebml.NewParser(f, 3).Parse(&collector{})
	ee, ok := err.(*ebml.Error)
	if !ok || ee.Code != ebml.CodeBadHeader {
		t.Fatalf("want EBML_BAD_HEADER, got %v", err)
	}
}
