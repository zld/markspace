package audio

import (
	"math"
	"testing"
)

func TestResamplerOutputFrameCount(t *testing.T) {
	cases := []struct{ in, out, n, want int64 }{
		{48000, 48000, 48000, 48000},
		{44100, 48000, 44100, 48000},
		{48000, 44100, 48000, 44100},
		{44100, 48000, 4410, 4800},
		{48000, 44100, 1, 1},
		{22050, 48000, 22050, 48000},
	}
	for _, c := range cases {
		r := NewResampler(c.in, c.out, 1)
		frames := make([]float64, c.n)
		got := 0
		// Split the input into unequal chunks on purpose.
		for off := int64(0); off < c.n; {
			step := int64(997)
			if off+step > c.n {
				step = c.n - off
			}
			got += len(r.Process(frames[off:off+step], int(step))) / 1
			off += step
		}
		got += len(r.Flush())
		if int64(got) != c.want {
			t.Fatalf("%d->%d: got %d frames want %d", c.in, c.out, got, c.want)
		}
	}
}

func TestResamplerPreservesConstantSignal(t *testing.T) {
	for _, ch := range []int{1, 2} {
		r := NewResampler(44100, 48000, ch)
		n := 44100
		in := make([]float64, n*ch)
		for i := range in {
			in[i] = 0.5
		}
		out := append(r.Process(in, n), r.Flush()...)
		if len(out) != 48000*ch {
			t.Fatalf("ch=%d frames=%d", ch, len(out)/ch)
		}
		// Skip the kernel ramp-up and settling tail.
		for i := (ch * 64); i < len(out)-(ch*64); i++ {
			if math.Abs(out[i]-0.5) > 1e-3 {
				t.Fatalf("ch=%d sample %d = %f", ch, i, out[i])
			}
		}
	}
}

func TestResamplerStereoChannelsStaySeparate(t *testing.T) {
	r := NewResampler(48000, 48000, 2)
	n := 4096
	in := make([]float64, n*2)
	for i := 0; i < n; i++ {
		in[2*i], in[2*i+1] = 0.25, -0.75
	}
	out := append(r.Process(in, n), r.Flush()...)
	if len(out) != n*2 {
		t.Fatalf("frames=%d", len(out)/2)
	}
	for i := 200; i < n-200; i++ {
		if math.Abs(out[2*i]-0.25) > 1e-6 || math.Abs(out[2*i+1]+0.75) > 1e-6 {
			t.Fatalf("frame %d = %v", i, out[2*i:2*i+2])
		}
	}
}

func TestResamplerReset(t *testing.T) {
	r := NewResampler(44100, 48000, 1)
	_ = r.Process(make([]float64, 1000), 1000)
	r.Reset()
	if got := len(r.Process(make([]float64, 441), 441)) + len(r.Flush()); got != 480 {
		t.Fatalf("flush after reset = %d", got)
	}
}

func TestResamplerValidatesConfig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on bad layout")
		}
	}()
	NewResampler(48000, 48000, 6)
}

func TestCommonRates(t *testing.T) {
	for _, rate := range []int64{8000, 44100, 48000, 96000} {
		if !IsCommonRate(rate) {
			t.Fatalf("%d rejected", rate)
		}
	}
	if IsCommonRate(12345) {
		t.Fatal("unusual rate accepted")
	}
}
