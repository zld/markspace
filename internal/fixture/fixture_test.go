package fixture

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"archivecheck/internal/merr"
)

func testCfg(scenario string, window int) Config {
	return Config{
		Scenario: scenario, Tracks: 3, Window: window,
		BandwidthKbps: []int{800, 420, 1100},
	}
}

func TestGenerateValidLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fx")
	fx, err := Generate(testCfg(ScenarioValid, 4), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.Tracks) != 3 {
		t.Fatalf("tracks = %d", len(fx.Tracks))
	}
	required := []string{
		filepath.Join("hls", "index.m3u8"),
		filepath.Join("dash", "manifest.mpd"),
		"fixture.json",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	for _, spec := range fx.Tracks {
		for seq := uint32(1); seq <= SegmentCount; seq++ {
			for _, suffix := range []string{".m4s", ".rtp", ".meta.json"} {
				name := "seg-" + uitoa(uint64(seq)) + suffix
				if _, err := os.Stat(filepath.Join(fx.TrackDir[spec.ID], name)); err != nil {
					t.Fatalf("missing track file %s: %v", name, err)
				}
			}
		}
	}
}

func TestGeneratedSegmentIsParsableFMP4(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fx")
	fx, _ := Generate(testCfg(ScenarioValid, 4), root)
	data, err := os.ReadFile(segmentPath(fx, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	found := map[string]bool{}
	var sampleCount uint32
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		found[h.BoxInfo.Type.String()] = true
		switch h.BoxInfo.Type {
		case mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			sampleCount = box.(*mp4.Trun).SampleCount
		case mp4.BoxTypeMoof(), mp4.BoxTypeTraf():
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		t.Fatalf("generated fMP4 not parseable: %v", err)
	}
	for _, want := range []string{"styp", "moof", "traf", "tfhd", "tfdt", "trun", "mdat"} {
		if !found[want] {
			t.Fatalf("generated segment missing box %s, got %v", want, found)
		}
	}
	if sampleCount != FramesPerSegment {
		t.Fatalf("trun sample count = %d, want %d", sampleCount, FramesPerSegment)
	}
}

func TestArchFrameRoundTrip(t *testing.T) {
	spec := FrameSpec{TrackID: 2, SegmentSeq: 3, Index: 7, Width: 128, Height: 72, Luma: 201, Size: 64}
	raw := BuildFrame(spec)
	got, err := ParseArchFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrackID != 2 || got.SegmentSeq != 3 || got.Index != 7 ||
		got.Width != 128 || got.Height != 72 || got.Luma != 201 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if _, err := ParseArchFrame(raw[:10]); err == nil {
		t.Fatal("short frame must be a decode error")
	}
	bad := append([]byte(nil), raw...)
	copy(bad[0:4], "XXXX")
	if _, err := ParseArchFrame(bad); err == nil {
		t.Fatal("bad magic must be an unsupported-container error")
	}
}

func readCapture(t *testing.T, path string) []*rtp.Packet {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[0:4], CaptureMagic[:]) {
		t.Fatalf("bad capture magic in %s", path)
	}
	var pkts []*rtp.Packet
	pos := captureHeaderLen
	for pos+4 <= len(data) {
		n := int(data[pos])<<24 | int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		pos += 4
		if pos+n > len(data) {
			t.Fatalf("truncated capture record in %s", path)
		}
		p := &rtp.Packet{}
		if err := p.Unmarshal(data[pos : pos+n]); err != nil {
			t.Fatalf("bad rtp datagram: %v", err)
		}
		pkts = append(pkts, p)
		pos += n
	}
	return pkts
}

func TestBurstLossCaptureHasGap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fx")
	fx, _ := Generate(testCfg(ScenarioBurstLoss, 2), root)
	validRoot := filepath.Join(t.TempDir(), "valid")
	validFx, _ := Generate(testCfg(ScenarioValid, 2), validRoot)

	lost := readCapture(t, capturePath(fx, 1, 1))
	good := readCapture(t, capturePath(validFx, 1, 1))
	if len(lost) >= len(good) {
		t.Fatalf("burst-loss capture must contain fewer packets: %d >= %d",
			len(lost), len(good))
	}
	if len(good)-len(lost) != burstLossCount {
		t.Fatalf("removed packets = %d, want %d", len(good)-len(lost), burstLossCount)
	}
	// Other tracks are unaffected.
	other := readCapture(t, capturePath(fx, 2, 1))
	goodOther := readCapture(t, capturePath(validFx, 2, 1))
	if len(other) != len(goodOther) {
		t.Fatal("only the first track should suffer loss")
	}
}

func TestTruncatedSegmentIsShorter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fx")
	fx, _ := Generate(testCfg(ScenarioTruncated, 4), root)
	validRoot := filepath.Join(t.TempDir(), "valid")
	validFx, _ := Generate(testCfg(ScenarioValid, 4), validRoot)
	bad, _ := os.ReadFile(segmentPath(fx, 1, 1))
	good, _ := os.ReadFile(segmentPath(validFx, 1, 1))
	if len(bad) >= len(good) {
		t.Fatal("truncated segment must be physically shorter")
	}
}

func TestDuplicateOrderReordersAndDeduplicates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fx")
	fx, _ := Generate(testCfg(ScenarioDuplicateOrder, 4), root)
	pkts := readCapture(t, capturePath(fx, 1, 1))
	seen := map[uint16]bool{}
	var inOrder []uint16
	prev := uint16(0)
	disorder := false
	for i, p := range pkts {
		if i > 0 && p.SequenceNumber < prev {
			disorder = true
		}
		prev = p.SequenceNumber
		seen[p.SequenceNumber] = true
	}
	if !disorder {
		t.Fatal("duplicate-order fixture should contain reordered packets")
	}
	if len(pkts) == len(seen) {
		t.Fatal("duplicate-order fixture should contain duplicate packets")
	}
	for s := range seen {
		inOrder = append(inOrder, s)
	}
	// Unique sequence set must be dense from zero.
	for i := range inOrder {
		if !seen[uint16(i)] {
			t.Fatalf("unique sequence set has a hole at %d", i)
		}
	}
}

func TestGenerationIsByteDeterministic(t *testing.T) {
	cfg := testCfg(ScenarioValid, 4)
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	fxA, _ := Generate(cfg, a)
	fxB, _ := Generate(cfg, b)
	for _, spec := range fxA.Tracks {
		for seq := uint32(1); seq <= SegmentCount; seq++ {
			da, _ := os.ReadFile(segmentPath(fxA, spec.ID, seq))
			db, _ := os.ReadFile(segmentPath(fxB, spec.ID, seq))
			if !bytes.Equal(da, db) {
				t.Fatalf("segment bytes differ across runs for track %d seq %d", spec.ID, seq)
			}
			ca, _ := os.ReadFile(capturePath(fxA, spec.ID, seq))
			cb, _ := os.ReadFile(capturePath(fxB, spec.ID, seq))
			if !bytes.Equal(ca, cb) {
				t.Fatal("capture bytes differ across runs")
			}
		}
	}
}

func TestValidateConfig(t *testing.T) {
	cases := []Config{
		{Scenario: "nope", Tracks: 1, BandwidthKbps: []int{100}, Window: 4},
		{Scenario: ScenarioValid, Tracks: 0, BandwidthKbps: nil, Window: 4},
		{Scenario: ScenarioValid, Tracks: 2, BandwidthKbps: []int{100}, Window: 4},
		{Scenario: ScenarioValid, Tracks: 1, BandwidthKbps: []int{0}, Window: 4},
		{Scenario: ScenarioValid, Tracks: 1, BandwidthKbps: []int{100}, Window: 0},
	}
	for i, cfg := range cases {
		if err := ValidateConfig(cfg); err == nil {
			t.Fatalf("case %d should be rejected", i)
		} else if me, ok := merr.As(err); !ok || me.Kind != merr.KindArgument {
			t.Fatalf("case %d wrong error kind: %v", i, err)
		}
	}
}
