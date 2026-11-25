package audio

import (
	"encoding/binary"
	"testing"
)

func mustConverter(t *testing.T, cfg Config) *Converter {
	t.Helper()
	c, err := NewConverter(cfg)
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	return c
}

func stereoBlock(frames int, v int16) []byte {
	out := make([]byte, frames*4)
	for i := 0; i < frames; i++ {
		binary.LittleEndian.PutUint16(out[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(out[i*4+2:], uint16(v))
	}
	return out
}

func TestLookupLayout(t *testing.T) {
	for _, name := range LayoutNames() {
		l, err := LookupLayout(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(l.Channels) == 0 {
			t.Fatalf("%s has no channels", name)
		}
	}
	if _, err := LookupLayout("quad"); err == nil {
		t.Fatal("unknown layout must fail")
	}
}

func TestChannelMapDownmix(t *testing.T) {
	fiveOne, _ := LookupLayout("5.1")
	stereo, _ := LookupLayout("stereo")
	m := ChannelMap(fiveOne, stereo)
	if len(m) != 2 || m[0].Target != "L" || len(m[0].Sources) != 1 {
		t.Fatalf("unexpected map: %+v", m)
	}
	mono, _ := LookupLayout("mono")
	m = ChannelMap(stereo, mono)
	if len(m) != 1 || len(m[0].Sources) != 2 {
		t.Fatalf("stereo->mono should fold both channels: %+v", m)
	}
	m = ChannelMap(stereo, fiveOne)
	if len(m) != 6 {
		t.Fatalf("5.1 target has %d entries", len(m))
	}
	if len(m[1].Sources) != 1 || m[1].Sources[0] != 1 {
		t.Fatalf("R should map to source R: %+v", m[1])
	}
	if len(m[3].Sources) != 0 {
		t.Fatalf("LFE must be silent when upmixing stereo: %+v", m[3])
	}
}

func TestConvertResamplesExact(t *testing.T) {
	c := mustConverter(t, Config{SourceRate: 48000, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
	cv := c.Start(48000)
	block := stereoBlock(48000, 16384)
	out, err := cv.ConvertBlock(block, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames != 48000 || out.Truncated {
		t.Fatalf("got %d frames truncated=%t", out.Frames, out.Truncated)
	}
	if len(out.PCM) != 48000*4 {
		t.Fatalf("pcm length %d", len(out.PCM))
	}
	if got := int16(binary.LittleEndian.Uint16(out.PCM[:2])); got != 16384 {
		t.Fatalf("first sample %d", got)
	}
}

func TestConvertDownsample(t *testing.T) {
	c := mustConverter(t, Config{SourceRate: 48000, SourceLayout: "stereo", TargetRate: 24000, TargetLayout: "stereo"})
	cv := c.Start(48000)
	out, err := cv.ConvertBlock(stereoBlock(48000, 1000), 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames != 24000 {
		t.Fatalf("got %d frames want 24000", out.Frames)
	}
}

func TestConvertUpsample(t *testing.T) {
	c := mustConverter(t, Config{SourceRate: 24000, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
	cv := c.Start(24000)
	out, err := cv.ConvertBlock(stereoBlock(24000, 2000), 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames != 48000 {
		t.Fatalf("got %d frames want 48000", out.Frames)
	}
}

func TestConvertBlocksTileWithoutGap(t *testing.T) {
	c := mustConverter(t, Config{SourceRate: 44100, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
	const blockFrames = 4410
	cv := c.Start(blockFrames * 10)
	var total int64
	for i := 0; i < 10; i++ {
		out, err := cv.ConvertBlock(stereoBlock(blockFrames, 100), int64(i*blockFrames), i, int64(i*blockFrames*4))
		if err != nil {
			t.Fatal(err)
		}
		if out.Start != total {
			t.Fatalf("block %d starts at %d, expected %d", i, out.Start, total)
		}
		total += int64(out.Frames)
	}
	if want := totalOut(blockFrames*10, 44100, 48000); total != want {
		t.Fatalf("total %d want %d", total, want)
	}
}

func TestConvertReportsTruncatedFrame(t *testing.T) {
	c := mustConverter(t, Config{SourceRate: 48000, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
	cv := c.Start(10)
	block := append(stereoBlock(2, 100), 0x01, 0x02) // one and a half frames
	out, err := cv.ConvertBlock(block, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Fatal("expected truncation report")
	}
	if out.Frames != 2 {
		t.Fatalf("usable frames %d", out.Frames)
	}
	if !cv.Truncated() {
		t.Fatal("conversion must remember truncation")
	}
}

func TestConvertRejectsOutOfOrder(t *testing.T) {
	c := mustConverter(t, Config{SourceRate: 48000, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
	cv := c.Start(96)
	if _, err := cv.ConvertBlock(stereoBlock(2, 1), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := cv.ConvertBlock(stereoBlock(2, 1), 0, 5, 0); err == nil {
		t.Fatal("out-of-order block must fail")
	}
}

func TestConvertMonoToStereo(t *testing.T) {
	c := mustConverter(t, Config{SourceRate: 48000, SourceLayout: "mono", TargetRate: 48000, TargetLayout: "stereo"})
	cv := c.Start(4)
	var mono []byte
	for i := 0; i < 4; i++ {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(5000))
		mono = append(mono, b[:]...)
	}
	out, err := cv.ConvertBlock(mono, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames != 4 {
		t.Fatalf("frames %d", out.Frames)
	}
	for i := 0; i < 4; i++ {
		l := int16(binary.LittleEndian.Uint16(out.PCM[i*4:]))
		r := int16(binary.LittleEndian.Uint16(out.PCM[i*4+2:]))
		if l != 5000 || r != 5000 {
			t.Fatalf("frame %d = %d,%d", i, l, r)
		}
	}
}

func TestNewConverterValidates(t *testing.T) {
	if _, err := NewConverter(Config{SourceRate: 0, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"}); err == nil {
		t.Fatal("zero source rate must fail")
	}
	if _, err := NewConverter(Config{SourceRate: 48000, SourceLayout: "quad", TargetRate: 48000, TargetLayout: "stereo"}); err == nil {
		t.Fatal("unknown layout must fail")
	}
}
