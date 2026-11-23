package audio

import (
	"testing"

	"mediacore/internal/sample"
	"mediacore/media"
)

func TestLayoutMappings(t *testing.T) {
	mono := []float32{1, -1, 0.5}
	st, err := Normalize(mono, LayoutMono, LayoutStereo)
	if err != nil || len(st) != 6 || st[0] != 1 || st[1] != 1 {
		t.Fatalf("mono->stereo: %v %v", st, err)
	}
	back, err := Normalize(st, LayoutStereo, LayoutMono)
	if err != nil {
		t.Fatal(err)
	}
	for i := range mono {
		if back[i] != mono[i] {
			t.Fatalf("round trip mismatch at %d: %v vs %v", i, back[i], mono[i])
		}
	}
}

func TestDownmix51(t *testing.T) {
	in := []float32{1, 1, 1, 0.5, 1, 1} // L R C LFE Ls Rs
	out, err := Normalize(in, LayoutSurround51, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	want := float32(1 + 0.7071 + 0.7071)
	if out[0] < want-1e-3 || out[0] > want+1e-3 {
		t.Fatalf("L' = %v, want ~%v", out[0], want)
	}
}

func TestUnknownLayoutRejected(t *testing.T) {
	if _, err := ParseLayout("quad"); media.CodeOf(err) != media.CodeLayoutUnknown {
		t.Fatalf("parse: %v", err)
	}
	if _, err := Normalize([]float32{1, 2}, LayoutUnknown, LayoutStereo); media.CodeOf(err) != media.CodeLayoutUnknown {
		t.Fatalf("normalize: %v", err)
	}
	if _, err := Normalize([]float32{1, 2}, LayoutStereo, LayoutSurround51); media.CodeOf(err) != media.CodeLayoutUnknown {
		t.Fatalf("unsupported pair must error, not silently upmix: %v", err)
	}
}

func TestResampleRates(t *testing.T) {
	for _, inRate := range []int{44100, 48000} {
		in := sample.Audio(1, inRate, 2, 1000)
		r, err := NewResampler(inRate, 48000, 2)
		if err != nil {
			t.Fatal(err)
		}
		out, err := r.Process(in)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r.Flush()...)
		got := len(out) / 2
		if got != 48000 {
			t.Fatalf("%d->48000: got %d frames", inRate, got)
		}
	}
}

func TestResampleStreamingContinuity(t *testing.T) {
	in := sample.Audio(2, 44100, 1, 500)
	whole, _ := NewResampler(44100, 48000, 1)
	a, _ := whole.Process(in)
	a = append(a, whole.Flush()...)

	chunked, _ := NewResampler(44100, 48000, 1)
	var b []float32
	for i := 0; i < len(in); i += 997 { // odd chunk size crosses frame phases
		end := i + 997
		if end > len(in) {
			end = len(in)
		}
		o, err := chunked.Process(in[i:end])
		if err != nil {
			t.Fatal(err)
		}
		b = append(b, o...)
	}
	b = append(b, chunked.Flush()...)
	if len(a) != len(b) {
		t.Fatalf("frame count differs: whole %d, chunked %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("sample %d differs: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestResampleTailPreserved(t *testing.T) {
	// 7 input frames at 8k -> 48k: tail interval must emit, not drop.
	in := []float32{0, 1, 0, -1, 0, 1, 0}
	r, _ := NewResampler(8000, 48000, 1)
	out, _ := r.Process(in)
	out = append(out, r.Flush()...)
	if len(out) != 42 { // round(7 * 48000/8000)
		t.Fatalf("got %d frames, want 42", len(out))
	}
}

func TestLimiter(t *testing.T) {
	in := sample.Audio(3, 48000, 2, 200)
	l := NewLimiter(2, 4800, 0.2, 0.95)
	out := l.Process(in)
	if len(out) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(out))
	}
	if l.InPeak <= 0 || l.OutPeak <= 0 {
		t.Fatalf("peaks not recorded: in=%v out=%v", l.InPeak, l.OutPeak)
	}
	if l.OutPeak > 0.95 {
		t.Fatalf("ceiling breached: %v", l.OutPeak)
	}
	if l.MinGain > l.MaxGain || l.LastGain <= 0 {
		t.Fatalf("gain telemetry broken: min=%v max=%v last=%v", l.MinGain, l.MaxGain, l.LastGain)
	}
}

func TestLimiterClipsHotSignal(t *testing.T) {
	// Sparse spikes: low window RMS drives gain up, peaks hit the ceiling.
	in := make([]float32, 4800*2)
	for i := 0; i < len(in); i += 200 {
		in[i] = 2.0 // over full scale
	}
	l := NewLimiter(2, 4800, 0.5, 0.9)
	out := l.Process(in)
	for _, s := range out {
		if s > 0.9 || s < -0.9 {
			t.Fatalf("output exceeds ceiling: %v", s)
		}
	}
	if l.OutPeak != 0.9 {
		t.Fatalf("OutPeak = %v, want 0.9", l.OutPeak)
	}
}
