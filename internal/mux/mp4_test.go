package mux

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"
)

func writeTestFile(t *testing.T, spec Spec) Info {
	t.Helper()
	dir := t.TempDir()
	spec.Path = filepath.Join(dir, "out.mp4")
	if spec.Rate == 0 {
		spec.Rate = 48000
	}
	if spec.Channels == 0 {
		spec.Channels = 2
	}
	if len(spec.ChunkFrames) == 0 {
		spec.ChunkFrames = []int{len(spec.PCM) / spec.Channels}
	}
	info, err := Write(spec)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	return info
}

func probe(t *testing.T, path string) (*mp4.ProbeInfo, os.FileInfo) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pi, err := mp4.Probe(f)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return pi, st
}

func TestWriteProducesParsableMP4(t *testing.T) {
	pcm := make([]int16, 4800*2)
	for i := range pcm {
		pcm[i] = int16(i % 1000)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp4")
	info, err := Write(Spec{Path: path, Rate: 48000, Channels: 2, PCM: pcm, ChunkFrames: []int{2400, 2400}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 4800 || info.Chunks != 2 || !info.IndexComplete {
		t.Fatalf("info = %+v", info)
	}
	pi, st := probe(t, path)
	if len(pi.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(pi.Tracks))
	}
	tr := pi.Tracks[0]
	if tr.Timescale != 48000 || tr.Duration != 4800 {
		t.Fatalf("track = %+v", tr)
	}
	if len(tr.Samples) != 4800 {
		t.Fatalf("samples = %d", len(tr.Samples))
	}
	if len(tr.Chunks) != 2 {
		t.Fatalf("chunks = %d", len(tr.Chunks))
	}
	if got := st.Size(); got < int64(4800*2*2) {
		t.Fatalf("file too small: %d", got)
	}
}

func TestWriteSampleDataMatchesPCM(t *testing.T) {
	pcm := []int16{1, -1, 256, -256, 32000, -32000}
	dir := t.TempDir()
	path := filepath.Join(dir, "b.mp4")
	if _, err := Write(Spec{Path: path, Rate: 48000, Channels: 2, PCM: pcm, ChunkFrames: []int{3}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Find the raw PCM bytes in big-endian 16-bit form.
	want := []byte{0x00, 0x01, 0xff, 0xff, 0x01, 0x00, 0xff, 0x00, 0x7d, 0x00, 0x83, 0x00}
	found := false
	for i := 0; i+len(want) <= len(raw); i++ {
		if string(raw[i:i+len(want)]) == string(want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("PCM payload not found verbatim in mdat")
	}
}

func TestWriteReportsMissingRanges(t *testing.T) {
	pcm := make([]int16, 960*2)
	dir := t.TempDir()
	path := filepath.Join(dir, "c.mp4")
	missing := []Range{{Start: 240, End: 480}}
	info, err := Write(Spec{
		Path: path, Rate: 48000, Channels: 2, PCM: pcm,
		ChunkFrames: []int{240, 240, 240, 240}, Missing: missing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.IndexComplete {
		t.Fatal("expected incomplete index when ranges are missing")
	}
	if len(info.Missing) != 1 || info.Missing[0].Start != 240 {
		t.Fatalf("missing = %+v", info.Missing)
	}
}

func TestWriteRejectsInconsistentChunks(t *testing.T) {
	_, err := Write(Spec{Path: filepath.Join(t.TempDir(), "d.mp4"), Rate: 48000, Channels: 2,
		PCM: make([]int16, 960*2), ChunkFrames: []int{100}})
	if err == nil {
		t.Fatal("expected chunk mismatch error")
	}
}

func TestWriteRejectsBadSpec(t *testing.T) {
	if _, err := Write(Spec{Path: "x.mp4", Rate: 0, Channels: 2, PCM: []int16{1}}); err == nil {
		t.Fatal("expected rate error")
	}
	if _, err := Write(Spec{Path: "x.mp4", Rate: 48000, Channels: 6, PCM: []int16{1}}); err == nil {
		t.Fatal("expected channel error")
	}
	if _, err := Write(Spec{Path: filepath.Join(t.TempDir(), "e.mp4"), Rate: 48000, Channels: 1}); err == nil {
		t.Fatal("expected empty audio error")
	}
}
