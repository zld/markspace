package report

import (
	"encoding/json"
	"testing"

	"mediasync/internal/render"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
)

func sampleInputs() (rtp.RecoveryStats, *sync.Result, []render.Frame, []render.AudioFragment) {
	stats := rtp.RecoveryStats{
		TotalPackets: 10, Corrupt: 1, Duplicates: 1, Late: 0, Missing: 1, Emitted: 7,
		HighWater: 3, Gaps: []rtp.Gap{{Start: 7, End: 7, Count: 1}},
		JitterTarget: 16, JitterMin: 0, JitterMax: 3, SmoothedBPS: 1_500_000,
	}
	align := &sync.Result{
		MaxDriftTick: 300, MaxDriftMs: 3, Cues: make([]sync.AlignedCue, 4),
		Drifts: make([]sync.Drift, 2),
	}
	frames := []render.Frame{
		{Index: 0, SHA256: "aaa", PixelMin: 1, PixelMax: 250, PixelMean: 120.5, PixelStd: 40.2, StartTick: 0},
		{Index: 1, SHA256: "bbb", PixelMin: 2, PixelMax: 251, PixelMean: 121.5, PixelStd: 41.2, StartTick: 3000},
	}
	audio := []render.AudioFragment{
		{Index: 0, SHA256: "ccc", PeakAmplitude: 9000, PeakLoudness: -11.2, StartTick: 0},
	}
	return stats, align, frames, audio
}

func TestBuildAndStableJSON(t *testing.T) {
	stats, align, frames, audio := sampleInputs()
	s1 := Build(7, stats, align, frames, audio)
	s2 := Build(7, stats, align, frames, audio)
	j1, err := s1.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	j2, err := s2.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(j1) != string(j2) {
		t.Fatal("JSON encoding not stable across builds")
	}
	// Must be valid JSON and round-trip back to the same hash.
	var raw map[string]any
	if err := json.Unmarshal(j1, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	h1, _ := s1.Hash()
	h2, _ := s2.Hash()
	if h1 != h2 {
		t.Fatal("summary hashes must match for identical inputs")
	}
	if s1.Packets.Missing != 1 || s1.Packets.JitterTarget != 16 {
		t.Fatalf("packet summary wrong: %+v", s1.Packets)
	}
	if s1.Alignment.MaxDriftMs != 3 || s1.Alignment.DriftSamples != 2 {
		t.Fatalf("alignment summary wrong: %+v", s1.Alignment)
	}
	if len(s1.Frames) != 2 || s1.Frames[1].SHA256 != "bbb" {
		t.Fatalf("frame summary wrong: %+v", s1.Frames)
	}
	if s1.Audio[0].PeakLoudness != -11.2 {
		t.Fatalf("audio summary wrong: %+v", s1.Audio)
	}
}

func TestCompareEqualAndMismatch(t *testing.T) {
	stats, align, frames, audio := sampleInputs()
	a := Build(7, stats, align, frames, audio)
	b := Build(7, stats, align, frames, audio)
	if err := Compare(a, b); err != nil {
		t.Fatalf("identical summaries must compare equal: %v", err)
	}

	// Mutate a frame hash: comparison must locate the frames section.
	stats2, align2, frames2, audio2 := sampleInputs()
	frames2[0].SHA256 = "ddd"
	c := Build(7, stats2, align2, frames2, audio2)
	err := Compare(a, c)
	if err == nil {
		t.Fatal("changed frame hash must be detected")
	}
	mm, ok := err.(*Mismatch)
	if !ok || mm.Field != "frames" {
		t.Fatalf("want frames mismatch, got %v", err)
	}

	// Mutate packet counters.
	stats3, align3, frames3, audio3 := sampleInputs()
	stats3.Missing = 2
	d := Build(7, stats3, align3, frames3, audio3)
	if err := Compare(a, d); err == nil || err.(*Mismatch).Field != "packets" {
		t.Fatalf("want packets mismatch, got %v", err)
	}
}

func TestCompareDetectsAudioAndAlignment(t *testing.T) {
	stats, align, frames, audio := sampleInputs()
	base := Build(7, stats, align, frames, audio)

	stats2, align2, frames2, audio2 := sampleInputs()
	audio2[0].PeakAmplitude = 9999
	got := Compare(base, Build(7, stats2, align2, frames2, audio2))
	if got == nil || got.(*Mismatch).Field != "audio" {
		t.Fatalf("want audio mismatch, got %v", got)
	}

	stats3, align3, frames3, audio3 := sampleInputs()
	align3.MaxDriftMs = 99
	got = Compare(base, Build(7, stats3, align3, frames3, audio3))
	if got == nil || got.(*Mismatch).Field != "alignment" {
		t.Fatalf("want alignment mismatch, got %v", got)
	}
}

func TestSortedHashesOrdersByIndex(t *testing.T) {
	s := &Summary{
		Frames: []FrameSummary{{Index: 2, SHA256: "f2"}, {Index: 0, SHA256: "f0"}, {Index: 1, SHA256: "f1"}},
		Audio:  []AudioSummary{{Index: 1, SHA256: "a1"}, {Index: 0, SHA256: "a0"}},
	}
	fh, ah := SortedHashes(s.Frames, s.Audio)
	wantF := []string{"f0", "f1", "f2"}
	wantA := []string{"a0", "a1"}
	for i := range wantF {
		if fh[i] != wantF[i] {
			t.Fatalf("frame hashes = %v, want %v", fh, wantF)
		}
	}
	for i := range wantA {
		if ah[i] != wantA[i] {
			t.Fatalf("audio hashes = %v, want %v", ah, wantA)
		}
	}
}

func TestSeedAffectsHash(t *testing.T) {
	stats, align, frames, audio := sampleInputs()
	h1, _ := Build(1, stats, align, frames, audio).Hash()
	h2, _ := Build(2, stats, align, frames, audio).Hash()
	if h1 == h2 {
		t.Fatal("different seeds must yield different summary hashes")
	}
}
