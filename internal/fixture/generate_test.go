package fixture

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loudscan/internal/ebu"
	"loudscan/internal/peak"
)

// readWAVFloats parses the files this package writes. It only needs to handle
// the exact layout in generate.go, so it doubles as a check on the header.
func readWAVFloats(t *testing.T, path string) (channels, rate int, samples []float64) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("%s is not a RIFF/WAVE file", path)
	}
	pos := 12
	for pos+8 <= len(b) {
		id := string(b[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(b[pos+4:]))
		body := b[pos+8 : pos+8+size]
		switch id {
		case "fmt ":
			format := binary.LittleEndian.Uint16(body[0:])
			if format != 0xFFFE {
				t.Fatalf("format tag = 0x%x, want extensible", format)
			}
			channels = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			if bits := binary.LittleEndian.Uint16(body[14:]); bits != 32 {
				t.Fatalf("bits = %d, want 32", bits)
			}
		case "data":
			for i := 0; i+4 <= len(body); i += 4 {
				samples = append(samples, float64(math.Float32frombits(binary.LittleEndian.Uint32(body[i:]))))
			}
		}
		pos += 8 + size + (size & 1)
	}
	return channels, rate, samples
}

func TestGenerateIsDeterministic(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	setA, err := Generate(dirA, 17)
	if err != nil {
		t.Fatal(err)
	}
	if setA.Dir != dirA || setA.Seed != 17 {
		t.Errorf("set metadata = %q/%d", setA.Dir, setA.Seed)
	}
	if _, err := Generate(dirB, 17); err != nil {
		t.Fatal(err)
	}
	for _, f := range setA.Files {
		a, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dirB, filepath.Base(f.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if len(a) != len(b) {
			t.Fatalf("%s: sizes differ", f.Path)
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("%s: byte %d differs between runs", f.Path, i)
			}
		}
	}
}

// TestFixturesMeasureAsIntended runs the real DSP over the generated files,
// without FFmpeg, so a bad calibration fails here rather than in a demo.
func TestFixturesMeasureAsIntended(t *testing.T) {
	dir := t.TempDir()
	set, err := Generate(dir, 17)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]File{}
	for _, f := range set.Files {
		byName[filepath.Base(f.Path)] = f
	}

	cases := []struct {
		name       string
		wantLUFS   float64
		wantJan    float64 // tolerance in LU
		wantPeakDB float64
		wantPeakOK bool
		checkPeak  bool
	}{
		{name: "stereo_sine_1khz.wav", wantLUFS: -23, wantJan: 0.2, checkPeak: true, wantPeakDB: -22.96},
		{name: "quad_side_44k.wav", wantLUFS: -23, wantJan: 0.2, checkPeak: true, wantPeakDB: -26.8},
		{name: "surround_5_1.wav", wantLUFS: -23, wantJan: 0.2},
		{name: "mono_center.wav", wantLUFS: -26, wantJan: 0.2},
		{name: "hot_stereo_peak.wav", wantLUFS: 4.0, wantJan: 0.3, checkPeak: true, wantPeakDB: 4.08},
	}
	for _, c := range cases {
		f, ok := byName[c.name]
		if !ok {
			t.Fatalf("fixture %s missing", c.name)
		}
		channels, rate, samples := readWAVFloats(t, f.Path)
		layout, err := ebu.NewLayout(strings.Split(f.Layout, "-"))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if layout.Len() != channels {
			t.Fatalf("%s: %d channels in fmt, %d in layout", c.name, channels, layout.Len())
		}
		meter, err := ebu.NewMeter(rate, layout)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		peakMeter, _ := peak.NewMeter(channels)
		if err := meter.ProcessInterleaved(samples); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := peakMeter.Process(samples); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		res := meter.Finish()
		if math.Abs(res.IntegratedLUFS-c.wantLUFS) > c.wantJan {
			t.Errorf("%s: integrated %.2f LUFS, want %.2f", c.name, res.IntegratedLUFS, c.wantLUFS)
		}
		if c.checkPeak {
			p := peakMeter.Finish()
			if math.Abs(p.DBTP-c.wantPeakDB) > 0.15 {
				t.Errorf("%s: true peak %.2f dBTP, want %.2f", c.name, p.DBTP, c.wantPeakDB)
			}
		}
	}
}

func TestSilenceFixture(t *testing.T) {
	dir := t.TempDir()
	set, err := Generate(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	var layoutName string
	for _, f := range set.Files {
		if strings.Contains(f.Path, "silence") {
			path, layoutName = f.Path, f.Layout
		}
	}
	if path == "" {
		t.Fatal("silence fixture missing")
	}
	channels, rate, samples := readWAVFloats(t, path)
	layout, _ := ebu.NewLayout(strings.Split(layoutName, "-"))
	if layout.Len() != channels {
		t.Fatal("layout mismatch")
	}
	m, _ := ebu.NewMeter(rate, layout)
	if err := m.ProcessInterleaved(samples); err != nil {
		t.Fatal(err)
	}
	if m.Finish().Measured {
		t.Fatal("silence measured")
	}
}

func TestChannelMask(t *testing.T) {
	if got := channelMask([]string{"FL", "FR", "FC", "LFE", "BL", "BR"}); got != 0x3F {
		t.Errorf("5.1 mask = 0x%X, want 0x3F", got)
	}
	if got := channelMask([]string{"FL", "FR", "SL", "SR"}); got != 0x603 {
		t.Errorf("quad(side) mask = 0x%X, want 0x603", got)
	}
}

func TestGenerateCreatesNestedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if _, err := Generate(dir, 17); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stereo_sine_1khz.wav")); err != nil {
		t.Fatal(err)
	}
}
