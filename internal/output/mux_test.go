package output

import (
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"

	"mediapipeline/internal/clock"
)

func audioTrack(n int) *Track {
	s := make([]Sample, n)
	for i := range s {
		ticks := uint32(960)
		if i%2 == 1 {
			ticks = 480 // variable-length frames
		}
		s[i] = Sample{Data: []byte{byte(i), byte(i + 1), byte(i + 2)}, Ticks: ticks}
	}
	return &Track{Kind: clock.KindAudio, TickRate: 48000, Timescale: 48000, Samples: s}
}

func videoTrack(n int) *Track {
	s := make([]Sample, n)
	for i := range s {
		s[i] = Sample{Data: []byte{0xAA, 0xBB, 0xCC, 0xDD}, Ticks: 1600}
	}
	return &Track{Kind: clock.KindVideo, TickRate: 48000, Timescale: 90000, Samples: s}
}

func TestWriteDeterministicAndReadable(t *testing.T) {
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "a.mp4"), filepath.Join(dir, "b.mp4")
	sum1, err := Write(p1, audioTrack(4), videoTrack(3))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	sum2, err := Write(p2, audioTrack(4), videoTrack(3))
	if err != nil {
		t.Fatalf("write2: %v", err)
	}
	if sum1.SHA256 != sum2.SHA256 || sum1.Size != sum2.Size {
		t.Fatalf("non-deterministic output: %+v vs %+v", sum1, sum2)
	}
	if sum1.AudioCount != 0 { // counts are filled by higher layers; mux leaves zero
		t.Fatalf("unexpected audio count: %d", sum1.AudioCount)
	}
	if info, err := os.Stat(p1); err != nil || info.Size() != sum1.Size {
		t.Fatalf("file size mismatch: %v", err)
	}

	// The produced file must parse as a valid MP4 with moov/mdat/ftyp.
	f, err := os.Open(p1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	found := map[string]bool{}
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		found[h.BoxInfo.Type.String()] = true
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		t.Fatalf("output not a readable MP4: %v", err)
	}
	for _, want := range []string{"ftyp", "moov", "mdat"} {
		if !found[want] {
			t.Fatalf("missing required box %q in %v", want, found)
		}
	}
}

func TestAudioOnlyTrack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mp4")
	sum, err := Write(path, audioTrack(2), nil)
	if err != nil {
		t.Fatalf("audio-only write: %v", err)
	}
	if sum.SHA256 == "" {
		t.Fatal("missing digest")
	}
}

func TestRejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write("", audioTrack(1), nil); err == nil {
		t.Fatal("empty path must be rejected")
	}
	badRate := audioTrack(1)
	badRate.TickRate = 0
	if _, err := Write(filepath.Join(dir, "x.mp4"), badRate, nil); err == nil {
		t.Fatal("zero tick rate must be rejected")
	}
	if _, err := Write(filepath.Join(dir, "y.mp4"), &Track{TickRate: 48000, Timescale: 48000}, nil); err == nil {
		t.Fatal("empty sample list must be rejected")
	}
	tooMany := audioTrack(MaxSamplesPerTrack + 1)
	if _, err := Write(filepath.Join(dir, "z.mp4"), tooMany, nil); err == nil {
		t.Fatal("oversized track must be rejected")
	}
}

func TestScaledDeltaExact(t *testing.T) {
	// 960 ticks at 48k -> 1800 units at 90k (the 90 kHz video timescale).
	if got := scaledDelta(960, 48000, 90000); got != 1800 {
		t.Fatalf("scaled delta = %d, want 1800", got)
	}
	// Identity scaling when timescales match.
	if got := scaledDelta(480, 48000, 48000); got != 480 {
		t.Fatalf("identity delta = %d, want 480", got)
	}
}
