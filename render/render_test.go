package render

import (
	"bytes"
	"math"
	"testing"

	"mediarc/model"
)

func timeline(n int) Timeline {
	slots := make([]Slot, n)
	for i := range slots {
		kind := SlotMedia
		if i%4 == 3 {
			kind = SlotSilence
		}
		slots[i] = Slot{Kind: kind, StartNS: int64(i) * 20_000_000, DurationNS: 20_000_000, Seq: uint64(i)}
	}
	return Timeline{Slots: slots}
}

func mustRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestRenderFramesDeterministic(t *testing.T) {
	r := mustRenderer(t)
	first := r.RenderFrames(timeline(8))
	second := mustRenderer(t).RenderFrames(timeline(8))
	if len(first) != 8 || len(second) != 8 {
		t.Fatalf("frame counts %d, %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Hash != second[i].Hash {
			t.Fatalf("frame %d hash %x != %x", i, first[i].Hash, second[i].Hash)
		}
		if !bytes.Equal(pixelsToBytes(first[i].Pixels), pixelsToBytes(second[i].Pixels)) {
			t.Fatalf("frame %d pixels differ", i)
		}
		if first[i].StartNS != int64(i)*20_000_000 {
			t.Fatalf("frame %d starts at %d", i, first[i].StartNS)
		}
	}
	if m1, m2 := Measure(first, nil), Measure(second, nil); m1.FrameHash != m2.FrameHash {
		t.Fatalf("combined hashes differ: %x != %x", m1.FrameHash, m2.FrameHash)
	}
}

func pixelsToBytes(pixels []RGB) []byte {
	out := make([]byte, 0, len(pixels)*3)
	for _, pixel := range pixels {
		out = append(out, pixel.R, pixel.G, pixel.B)
	}
	return out
}

func TestFramesDistinctPerSlot(t *testing.T) {
	r := mustRenderer(t)
	frames := r.RenderFrames(timeline(6))
	seen := map[uint64]bool{}
	for _, frame := range frames {
		if seen[frame.Hash] {
			t.Fatalf("duplicate frame hash %x", frame.Hash)
		}
		seen[frame.Hash] = true
	}
}

func TestRenderSortsSlotsDeterministically(t *testing.T) {
	r := mustRenderer(t)
	ordered := timeline(5)
	shuffled := Timeline{Slots: []Slot{ordered.Slots[3], ordered.Slots[0], ordered.Slots[4], ordered.Slots[1], ordered.Slots[2]}}
	a := r.RenderFrames(ordered)
	b := r.RenderFrames(shuffled)
	for i := range a {
		if a[i].Hash != b[i].Hash {
			t.Fatalf("frame %d differs after reordering input", i)
		}
	}
}

func TestPixelStats(t *testing.T) {
	r := mustRenderer(t)
	frames := r.RenderFrames(timeline(8))
	metrics := Measure(frames, nil)
	stats := metrics.Pixels
	if stats.Frames != 8 {
		t.Fatalf("frames = %d", stats.Frames)
	}
	if stats.MeanLuma <= 0 || stats.MeanLuma >= 255 {
		t.Fatalf("mean luma = %f", stats.MeanLuma)
	}
	if stats.MinLuma > stats.MeanLuma || stats.MeanLuma > stats.MaxLuma {
		t.Fatalf("luma ordering: min=%f mean=%f max=%f", stats.MinLuma, stats.MeanLuma, stats.MaxLuma)
	}
	if stats.RecoveredFrames != 2 {
		t.Fatalf("recovered frames = %d, want 2", stats.RecoveredFrames)
	}
	if metrics.FrameBytes != int64(8*64*36*3) {
		t.Fatalf("frame bytes = %d", metrics.FrameBytes)
	}
	// Channel means must actually be computed from the pixels.
	var wantR float64
	for _, frame := range frames {
		for _, pixel := range frame.Pixels {
			wantR += float64(pixel.R)
		}
	}
	wantR /= float64(8 * 64 * 36)
	if math.Abs(stats.MeanR-wantR) > 1e-9 {
		t.Fatalf("mean R = %f, want %f", stats.MeanR, wantR)
	}
}

func TestAudioDeterministicAndLoudness(t *testing.T) {
	r := mustRenderer(t)
	first := r.RenderAudio(timeline(4))
	second := mustRenderer(t).RenderAudio(timeline(4))
	for i := range first {
		if first[i].Hash != second[i].Hash {
			t.Fatalf("audio %d hash differs", i)
		}
	}
	metrics := Measure(nil, first)
	if metrics.Audio.Chunks != 4 {
		t.Fatalf("chunks = %d", metrics.Audio.Chunks)
	}
	// Media chunks are a half-scale sine: about -6 dBFS peak.
	if metrics.Audio.PeakDBFS > -5.5 || metrics.Audio.PeakDBFS < -6.5 {
		t.Fatalf("peak = %.2f dBFS, want about -6", metrics.Audio.PeakDBFS)
	}
	// One slot of four is silence.
	if metrics.Audio.SilenceChunks != 1 {
		t.Fatalf("silent chunks = %d, want 1", metrics.Audio.SilenceChunks)
	}
	if metrics.Audio.RecoveredChunks != 1 {
		t.Fatalf("recovered chunks = %d, want 1", metrics.Audio.RecoveredChunks)
	}
	if metrics.Audio.RMSDBFS > metrics.Audio.PeakDBFS {
		t.Fatalf("RMS %.2f above peak %.2f", metrics.Audio.RMSDBFS, metrics.Audio.PeakDBFS)
	}
	// 20 ms at 48 kHz stereo, 16-bit.
	wantBytes := int64(20_000_000 * 48000 / 1_000_000_000 * 2 * 2)
	if metrics.AudioBytes != wantBytes*4 {
		t.Fatalf("audio bytes = %d, want %d", metrics.AudioBytes, wantBytes*4)
	}
}

func TestAllSilenceHasFloorLoudness(t *testing.T) {
	r := mustRenderer(t)
	silent := Timeline{Slots: []Slot{
		{Kind: SlotSilence, StartNS: 0, DurationNS: 20_000_000, Seq: 1},
		{Kind: SlotSilence, StartNS: 20_000_000, DurationNS: 20_000_000, Seq: 2},
	}}
	metrics := Measure(nil, r.RenderAudio(silent))
	if metrics.Audio.PeakDBFS != -96 {
		t.Fatalf("silence peak = %f, want -96", metrics.Audio.PeakDBFS)
	}
	if metrics.Audio.SilenceChunks != 2 {
		t.Fatalf("silent chunks = %d", metrics.Audio.SilenceChunks)
	}
}

func TestRenderConfigValidation(t *testing.T) {
	cases := []Config{
		{Canvas: Canvas{Width: 0, Height: 10}},
		{Canvas: Canvas{Width: 10, Height: -1}},
		{Canvas: Canvas{Width: 5000, Height: 10}},
		{SampleRate: -1},
		{Channels: -2},
		{FrameDurationNS: -5},
	}
	for _, cfg := range cases {
		if _, err := New(cfg); !model.IsCode(err, model.CodeInvalidArgument) {
			t.Fatalf("config %+v: %v", cfg, err)
		}
	}
	r, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config().SampleRate != 48000 || r.Config().Channels != 2 {
		t.Fatalf("defaults = %+v", r.Config())
	}
}

func TestSummariseRecordedHashes(t *testing.T) {
	r := mustRenderer(t)
	frames := r.RenderFrames(timeline(5))
	chunks := r.RenderAudio(timeline(5))
	summary := Summarise(frames, chunks)
	if summary.FrameCount != 5 || summary.AudioCount != 5 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if summary.FirstHash != frames[0].Hash || summary.LastHash != frames[4].Hash {
		t.Fatal("summary hashes do not match frames")
	}
	if summary.RecoveredNS != 20_000_000 {
		t.Fatalf("recovered ns = %d", summary.RecoveredNS)
	}
	if summary.FrameHash == 0 || summary.AudioHash == 0 {
		t.Fatal("combined hashes should be non-zero")
	}
}

func TestEmptyTimeline(t *testing.T) {
	r := mustRenderer(t)
	frames := r.RenderFrames(Timeline{})
	chunks := r.RenderAudio(Timeline{})
	if len(frames) != 0 || len(chunks) != 0 {
		t.Fatal("empty timeline produced output")
	}
	metrics := Measure(frames, chunks)
	if metrics.Pixels.Frames != 0 || metrics.Audio.Chunks != 0 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestFormatLoudness(t *testing.T) {
	if got := FormatLoudness(-6.0206); got != "-6.02 dBFS" {
		t.Fatalf("FormatLoudness = %q", got)
	}
}
