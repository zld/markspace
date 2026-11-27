package fixture

import (
	"os"
	"testing"

	"gamepack/internal/source"
	"gamepack/internal/stream"
)

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

func TestGenerateScannableForEachCodec(t *testing.T) {
	for _, codec := range []stream.Codec{stream.CodecH264, stream.CodecH265, stream.CodecAV1} {
		dir := t.TempDir()
		art, err := Generate(dir, Options{
			Codec: codec, BitrateKbps: 1000, Units: 12, KeyEvery: 4,
			WrapBits: 16, Injections: map[Injection]bool{},
		})
		if err != nil {
			t.Fatalf("%s generate: %v", codec, err)
		}
		res, err := stream.ScanFile(codec, art.StreamPath, art.IndexPath)
		if err != nil {
			t.Fatalf("%s scan: %v", codec, err)
		}
		if len(res.Units) != 12 || len(res.Boundaries) != 2 {
			t.Fatalf("%s units=%d boundaries=%d", codec, len(res.Units), len(res.Boundaries))
		}
		pkts, err := stream.ReadRTPSource(art.RTPPath)
		if err != nil || len(pkts) != 12 {
			t.Fatalf("%s rtp: %v len=%d", codec, err, len(pkts))
		}
	}
}

func TestSwitchAndWrapProduceValidNormalization(t *testing.T) {
	dir := t.TempDir()
	art, err := Generate(dir, Options{
		Codec: stream.CodecH264, BitrateKbps: 800, Units: 12, KeyEvery: 3,
		WrapBits: 16,
		Injections: map[Injection]bool{InjectSwitch: true, InjectWrap: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := stream.ScanFile(stream.CodecH264, art.StreamPath, art.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	pkts, err := stream.ReadRTPSource(art.RTPPath)
	if err != nil {
		t.Fatal(err)
	}
	norm, err := source.NewNormalizer(source.Config{BitWidth: 16})
	if err != nil {
		t.Fatal(err)
	}
	sawSwitch, sawWrap := false, false
	for i, p := range pkts {
		if i == len(pkts)/2 {
			norm.SwitchAt()
		}
		if err := norm.Push(i, p.Timestamp, res.Units[i].Start); err != nil {
			t.Fatalf("normalize unit %d: %v", i, err)
		}
	}
	for _, s := range norm.Samples() {
		sawSwitch = sawSwitch || s.Switched
		sawWrap = sawWrap || s.Wrapped
	}
	if !sawSwitch || !sawWrap {
		t.Fatalf("expected switch=%v wrap=%v", sawSwitch, sawWrap)
	}
	// Normalized ticks must be monotonic despite raw discontinuities.
	prev := norm.Samples()[0].NormalizedTick
	for _, s := range norm.Samples()[1:] {
		if s.NormalizedTick < prev {
			t.Fatal("normalized timeline regressed")
		}
		prev = s.NormalizedTick
	}
}

func TestTruncatedInjectionFailsScan(t *testing.T) {
	dir := t.TempDir()
	art, err := Generate(dir, Options{
		Codec: stream.CodecH264, BitrateKbps: 500, Units: 6, KeyEvery: 3,
		WrapBits: 16, Injections: map[Injection]bool{InjectTruncated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if art.TruncatedPath == "" {
		t.Fatal("truncated artifact paths must be populated")
	}
	if _, err := stream.ScanFile(stream.CodecH264,
		art.TruncatedPath, art.TruncatedIndex); err == nil {
		t.Fatal("truncated fixture must fail scanning")
	}
}

func TestDeterministicGeneration(t *testing.T) {
	opt := Options{
		Codec: stream.CodecAV1, BitrateKbps: 300, Units: 9, KeyEvery: 3,
		WrapBits: 16, Injections: map[Injection]bool{InjectWrap: true},
	}
	a, err := Generate(t.TempDir(), opt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(t.TempDir(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if !filesEqual(a.StreamPath, b.StreamPath) || !filesEqual(a.RTPPath, b.RTPPath) {
		t.Fatal("identical options must generate byte-identical artifacts")
	}
}

func filesEqual(x, y string) bool {
	bx, _ := readFile(x)
	by, _ := readFile(y)
	if len(bx) != len(by) {
		return false
	}
	for i := range bx {
		if bx[i] != by[i] {
			return false
		}
	}
	return true
}
