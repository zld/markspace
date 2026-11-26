package fixture

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/scan"
)

func TestSidecarsAreParseable(t *testing.T) {
	dir := t.TempDir()
	if err := writeWebVTT(filepath.Join(dir, "s.vtt"), 3); err != nil {
		t.Fatal(err)
	}
	if err := writeChapters(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	if err := writeID3(filepath.Join(dir, "t.id3")); err != nil {
		t.Fatal(err)
	}
	if err := writeRTPSEI(filepath.Join(dir, "r.rtp"), 3); err != nil {
		t.Fatal(err)
	}

	vtt, _ := os.ReadFile(filepath.Join(dir, "s.vtt"))
	res, err := scan.ScanWebVTT(bytes.NewReader(vtt), "s.vtt", "h", 1)
	if err != nil || len(res.Events) != 3 {
		t.Fatalf("webvtt events=%d err=%v", len(res.Events), err)
	}

	ch, _ := os.ReadFile(filepath.Join(dir, "c.txt"))
	cres, err := scan.ScanChapters(bytes.NewReader(ch), "c.txt", "h", 2)
	if err != nil || len(cres.Events) != 2 {
		t.Fatalf("chapter events=%d err=%v", len(cres.Events), err)
	}

	id3, _ := os.ReadFile(filepath.Join(dir, "t.id3"))
	ires, err := scan.ScanID3(bytes.NewReader(id3), "t.id3", "h", 3)
	if err != nil || len(ires.Events) != 2 {
		t.Fatalf("id3 events=%d err=%v", len(ires.Events), err)
	}
	if ires.Events[0].Tick != 1500 {
		t.Fatalf("id3 tick = %d", ires.Events[0].Tick)
	}

	rtp, _ := os.ReadFile(filepath.Join(dir, "r.rtp"))
	pkts, err := ReadRTPStream(rtp)
	if err != nil || len(pkts) != 3 {
		t.Fatalf("rtp packets=%d err=%v", len(pkts), err)
	}
	x := scan.NewRTPSEIExtractor("h264", 4)
	for i, p := range pkts {
		if err := x.Push(p, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(x.Events) != 3 {
		t.Fatalf("sei events=%d anomalies=%+v", len(x.Events), x.Anomalies)
	}
}

func TestRTPStreamFramingTruncation(t *testing.T) {
	good := append(u32be(2), 0x80, 0x60)
	if _, err := ReadRTPStream(good); err != nil {
		t.Fatalf("valid frame should parse: %v", err)
	}
	bad := append(u32be(99), 0x80) // declares more bytes than present
	if _, err := ReadRTPStream(bad); err == nil {
		t.Fatal("truncated frame must error")
	}
}

func TestMaterializeScenarioMutations(t *testing.T) {
	base := &Bundle{
		MP4Path:     filepath.Join(t.TempDir(), "src.fmp4"),
		ReplacePath: filepath.Join(t.TempDir(), "repl.fmp4"),
	}
	// Minimal fragmented-MP4-ish blob containing an mdat box.
	mdatPayload := make([]byte, 64)
	blob := append([]byte{0, 0, 0, 8, 'f', 't', 'y', 'p'},
		append([]byte{0, 0, 0, byte(72), 'm', 'd', 'a', 't'}, mdatPayload...)...)
	if err := os.WriteFile(base.MP4Path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	// A distinct, valid replacement blob.
	repl := append([]byte{}, blob...)
	for i := range repl {
		repl[i] ^= 0x5A
	}
	if err := os.WriteFile(base.ReplacePath, repl, 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	paths, err := Materialize(base, work, AllScenarios)
	if err != nil {
		t.Fatal(err)
	}
	normal, _ := os.ReadFile(paths[ScenarioNormal])
	damage, _ := os.ReadFile(paths[ScenarioDamage])
	meta, _ := os.ReadFile(paths[ScenarioMetadata])
	replaced, _ := os.ReadFile(paths[ScenarioReplace])

	if string(normal) != string(blob) {
		t.Fatal("normal scenario must be byte-identical to base")
	}
	if string(damage) == string(blob) {
		t.Fatal("damage scenario must alter bytes")
	}
	// Replace scenario is the distinct replacement content, same size.
	if string(replaced) != string(repl) {
		t.Fatal("replace scenario must use the replacement asset")
	}
	// Metadata scenario appends exactly a 16-byte free box, prefix unchanged.
	if len(meta) != len(blob)+16 || string(meta[:len(blob)]) != string(blob) {
		t.Fatal("metadata edit must only append a free box")
	}
	if string(meta[len(blob)+4:len(blob)+8]) != "free" {
		t.Fatal("appended box must be 'free'")
	}
}

func TestMaterializeUnknownScenario(t *testing.T) {
	base := &Bundle{MP4Path: filepath.Join(t.TempDir(), "x.fmp4")}
	os.WriteFile(base.MP4Path, []byte{0, 0, 0, 8, 'f', 'r', 'e', 'e'}, 0o644)
	if _, err := Materialize(base, t.TempDir(), []string{"nope"}); err == nil {
		t.Fatal("unknown scenario must error")
	}
}
