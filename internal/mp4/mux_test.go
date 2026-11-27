package mp4

import (
	"bytes"
	"testing"

	"github.com/example/mediad/internal/audio"
)

func TestMuxAndVerifyStereo(t *testing.T) {
	in := audio.NewBuffer(1000, 2)
	for i := range in.Samples {
		in.Samples[i] = 0.25
	}
	cfg := MuxConfig{Rate: 48000, Channels: 2, Codec: "mp4a"}
	out, rep, err := Mux(in, cfg)
	if err != nil {
		t.Fatalf("mux: %v", err)
	}
	if len(out) == 0 || int64(len(out)) != rep.Bytes {
		t.Fatalf("byte accounting wrong: len=%d report=%d", len(out), rep.Bytes)
	}
	if rep.SampleCount != 1000 {
		t.Fatalf("sample count = %d, want 1000", rep.SampleCount)
	}
	if rep.Timescale != 48000 || rep.SampleRate != 48000 || rep.ChannelCount != 2 {
		t.Fatalf("track descriptor wrong: %+v", rep)
	}
	if rep.TrackDuration != 1000 {
		t.Fatalf("duration = %d, want 1000", rep.TrackDuration)
	}
	// 1000 frames at 48k = 20833 us (rounded).
	if rep.DurationMicros != 1000*1_000_000/48000 {
		t.Fatalf("duration micros = %d", rep.DurationMicros)
	}
	if rep.AvgBitratebps <= 0 {
		t.Fatalf("bitrate = %v, want positive", rep.AvgBitratebps)
	}
	// A second independent Verify over the raw bytes must agree.
	rep2, err := Verify(bytes.NewReader(out), int64(len(out)), cfg)
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if rep2.SampleCount != rep.SampleCount || rep2.TrackDuration != rep.TrackDuration {
		t.Fatalf("non-deterministic verify: %+v vs %+v", rep2, rep)
	}
}

func TestMuxMono(t *testing.T) {
	in := audio.NewBuffer(500, 1)
	for i := range in.Samples {
		in.Samples[i] = -0.5
	}
	cfg := MuxConfig{Rate: 24000, Channels: 1, Codec: "mp4a"}
	out, rep, err := Mux(in, cfg)
	if err != nil {
		t.Fatalf("mux: %v", err)
	}
	if rep.ChannelCount != 1 || rep.SampleRate != 24000 {
		t.Fatalf("mono descriptor wrong: %+v", rep)
	}
	// PCM payload must be 2 bytes per sample per channel.
	wantPCM := 500 * 1 * 2
	if int64(wantPCM) > rep.Bytes {
		t.Fatalf("file %d bytes smaller than PCM payload %d", rep.Bytes, wantPCM)
	}
	if len(out) == 0 {
		t.Fatal("empty output")
	}
}

func TestMuxChannelMismatch(t *testing.T) {
	in := audio.NewBuffer(10, 1)
	if _, _, err := Mux(in, MuxConfig{Rate: 48000, Channels: 2, Codec: "mp4a"}); err == nil {
		t.Fatal("expected channel mismatch error")
	}
}

func TestSmallestWithinBudget(t *testing.T) {
	in := audio.NewBuffer(500, 2)
	for i := range in.Samples {
		in.Samples[i] = 0.1
	}
	base := MuxConfig{Rate: 48000, Channels: 2, Codec: "mp4a"}

	// A 24-bit mux is larger than a 16-bit mux.
	a16, _, err := Mux(in, base)
	if err != nil {
		t.Fatal(err)
	}
	a24, _, err := Mux(in, MuxConfig{Rate: 48000, Channels: 2, Codec: "mp4a", BitsPerSample: 24})
	if err != nil {
		t.Fatal(err)
	}
	if len(a24) <= len(a16) {
		t.Fatalf("24-bit artifact %d should exceed 16-bit %d", len(a24), len(a16))
	}

	// Generous budget: optimizer picks the smallest (16-bit) candidate.
	artifact, rep, fit, err := SmallestWithinBudget(in, base, 1<<20)
	if err != nil || !fit {
		t.Fatalf("expected a fitting candidate: fit=%v err=%v", fit, err)
	}
	if len(artifact) != len(a16) {
		t.Fatalf("optimizer chose %d bytes, want smallest %d", len(artifact), len(a16))
	}
	if rep.ChannelCount != 2 || rep.SampleRate != 48000 {
		t.Fatalf("report descriptor wrong: %+v", rep)
	}

	// Impossible budget: returns the smallest artifact but ok=false so the
	// caller marks the item unmet rather than degrading quality.
	smallest, _, fit, err := SmallestWithinBudget(in, base, 10)
	if err != nil {
		t.Fatal(err)
	}
	if fit {
		t.Fatal("no candidate should fit a 10-byte budget")
	}
	if len(smallest) != len(a16) {
		t.Fatalf("fallback should be the smallest 16-bit artifact")
	}
}
