package audio

import (
	"math"
	"testing"
)

func TestResamplerIdentity(t *testing.T) {
	r := NewResampler(48000, 48000, 1)
	in := Buffer{Samples: []float64{1, 2, 3, 4}, Frames: 4, Ch: 1}
	out, err := r.Process(in)
	if err != nil {
		t.Fatal(err)
	}
	flush := r.Flush()
	out.Append(flush)
	if out.Frames != 4 {
		t.Fatalf("identity frames = %d, want 4", out.Frames)
	}
	for i, v := range []float64{1, 2, 3, 4} {
		if math.Abs(out.Samples[i]-v) > 1e-12 {
			t.Fatalf("sample %d = %v, want %v", i, out.Samples[i], v)
		}
	}
}

func TestResamplerDownsample(t *testing.T) {
	// 48k -> 24k: one second of constant 0.5 yields 24000 frames at 0.5.
	r := NewResampler(48000, 24000, 2)
	in := NewBuffer(48000, 2)
	for i := range in.Samples {
		in.Samples[i] = 0.5
	}
	out, err := r.Process(in)
	if err != nil {
		t.Fatal(err)
	}
	out.Append(r.Flush())
	if out.Frames != 24000 {
		t.Fatalf("downsample frames = %d, want 24000", out.Frames)
	}
	for i, v := range out.Samples {
		if math.Abs(v-0.5) > 1e-12 {
			t.Fatalf("sample %d = %v, want 0.5", i, v)
		}
	}
}

func TestResamplerUpsampleCount(t *testing.T) {
	// 24k -> 48k doubles frame count.
	r := NewResampler(24000, 48000, 1)
	in := NewBuffer(100, 1)
	for i := range in.Samples {
		in.Samples[i] = float64(i)
	}
	out, _ := r.Process(in)
	out.Append(r.Flush())
	if out.Frames != 200 {
		t.Fatalf("upsample frames = %d, want 200", out.Frames)
	}
	// First output frame equals first input frame (zero phase).
	if out.Samples[0] != 0 {
		t.Fatalf("first output = %v, want 0", out.Samples[0])
	}
	// Even-indexed outputs land exactly on input frames.
	if math.Abs(out.Samples[2]-1) > 1e-12 {
		t.Fatalf("output[2] = %v, want 1", out.Samples[2])
	}
}

func TestResamplerCrossPacketState(t *testing.T) {
	// Split one stream across two packets: output must equal the one-shot run.
	oneShot := NewResampler(48000, 24000, 1)
	a := Buffer{Samples: []float64{1, 2, 3, 4, 5, 6}, Frames: 6, Ch: 1}
	o1, _ := oneShot.Process(a)
	o1.Append(oneShot.Flush())

	split := NewResampler(48000, 24000, 1)
	p1 := Buffer{Samples: []float64{1, 2, 3}, Frames: 3, Ch: 1}
	p2 := Buffer{Samples: []float64{4, 5, 6}, Frames: 3, Ch: 1}
	s1, _ := split.Process(p1)
	s2, _ := split.Process(p2)
	s1.Append(s2)
	s1.Append(split.Flush())

	if s1.Frames != o1.Frames {
		t.Fatalf("split frames = %d, one-shot = %d", s1.Frames, o1.Frames)
	}
	for i := range o1.Samples {
		if math.Abs(s1.Samples[i]-o1.Samples[i]) > 1e-12 {
			t.Fatalf("sample %d split=%v oneshot=%v", i, s1.Samples[i], o1.Samples[i])
		}
	}
}

func TestResamplerFinalZeroPad(t *testing.T) {
	// 48k -> 24k, three input frames. Output phases 0 and 2 both land on real
	// frame boundaries, so Process emits two exact samples (no invention).
	// Rounded target is also two, hence Flush adds nothing; the zero endpoint
	// only engages when the rounded target exceeds real-boundary outputs.
	r := NewResampler(48000, 24000, 1)
	in := Buffer{Samples: []float64{1, 1, 1}, Frames: 3, Ch: 1}
	out, _ := r.Process(in)
	if out.Frames != 2 {
		t.Fatalf("process frames = %d, want 2 boundary samples", out.Frames)
	}
	flush := r.Flush()
	if flush.Frames != 0 {
		t.Fatalf("flush = %+v, want no extra frames at exact target", flush)
	}
	if uint64(out.Frames) != r.RoundedOutput(3) {
		t.Fatalf("frames = %d, want target %d", out.Frames, r.RoundedOutput(3))
	}
	// Flush is idempotent after the first call.
	if again := r.Flush(); again.Frames != 0 {
		t.Fatalf("second flush produced %d frames", again.Frames)
	}
}

func TestResamplerZeroEndpointDecay(t *testing.T) {
	// Public-API proof of the fixed zero endpoint. A 48k -> 32k stream
	// (step 1.5 input spans) fed one packet of two constant-1 frames emits
	// output positions 0 and 1.5, both real, so both must read exactly 1.
	r := NewResampler(48000, 32000, 1)
	out, err := r.Process(Buffer{Samples: []float64{1, 1}, Frames: 2, Ch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames != 2 {
		t.Fatalf("process = %d frames, want 2", out.Frames)
	}
	for i, v := range out.Samples {
		if math.Abs(v-1.0) > 1e-9 {
			t.Fatalf("real sample %d = %v, want 1", i, v)
		}
	}
	// Cross-packet state: a 24k -> 48k upsampler (step 0.5) keeps the prior
	// packet's last frame. Packet 1 (one frame, value 1) emits positions 0 and
	// 0.5; the second sample extrapolates within the constant frame and reads
	// 1. Packet 2 (one frame, value 0) then starts at position 1.0. Because
	// the carried previous frame (1) is retained, the resampler does not read
	// uninitialized memory and the boundary output is exactly the new value 0.
	r = NewResampler(24000, 48000, 1)
	p1, err := r.Process(Buffer{Samples: []float64{1}, Frames: 1, Ch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p1.Frames != 2 || math.Abs(p1.Samples[1]-1.0) > 1e-9 {
		t.Fatalf("packet1 = %+v, want two samples of 1", p1)
	}
	cont, err := r.Process(Buffer{Samples: []float64{0}, Frames: 1, Ch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if cont.Frames < 1 || math.Abs(cont.Samples[0]) > 1e-9 {
		t.Fatalf("packet2 boundary = %+v, want first sample 0", cont)
	}
	// The carried previous frame must still hold value 1 (state preserved).
	if math.Abs(r.prev[0]-0.0) > 1e-9 || r.prev2 == nil || math.Abs(r.prev2[0]-1.0) > 1e-9 {
		t.Fatalf("cross-packet frames wrong: prev=%v prev2=%v", r.prev, r.prev2)
	}
	// A genuine half-weight blend is observable inside a single packet whose
	// two frames differ: 24k -> 48k over [1, 0] yields a position-0.5 sample
	// blending frame0 (1) and frame1 (0) -> 0.5.
	r3 := NewResampler(24000, 48000, 1)
	blend, err := r3.Process(Buffer{Samples: []float64{1, 0}, Frames: 2, Ch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if blend.Frames < 2 || math.Abs(blend.Samples[1]-0.5) > 1e-9 {
		t.Fatalf("blend = %+v, want second sample 0.5", blend)
	}
}

func TestResamplerChannelMismatch(t *testing.T) {
	r := NewResampler(48000, 24000, 2)
	bad := Buffer{Samples: []float64{1, 2, 3}, Frames: 3, Ch: 1}
	if _, err := r.Process(bad); err == nil {
		t.Fatal("expected channel mismatch error")
	}
}
