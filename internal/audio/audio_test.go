package audio

import (
	"math"
	"testing"
)

func sine(n, rate, freq int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(math.Sin(2*math.Pi*float64(freq)*float64(i)/float64(rate)) * 0.5 * 32767)
	}
	return out
}

func TestResampleRatio(t *testing.T) {
	in := sine(44100, 44100, 440) // 1 秒
	rs := NewResampler(44100, 48000, 1)
	out := rs.Process(in)
	out = append(out, rs.Flush()...)
	if got := len(out); got != 48000 {
		t.Fatalf("out samples=%d, want 48000", got)
	}
	// 确定性：相同输入重跑得相同输出
	rs2 := NewResampler(44100, 48000, 1)
	out2 := append(rs2.Process(in), rs2.Flush()...)
	for i := range out {
		if out[i] != out2[i] {
			t.Fatalf("nondeterministic at %d", i)
		}
	}
}

func TestResamplerChunkedEqualsWhole(t *testing.T) {
	in := sine(4410, 44100, 440)
	whole := append(NewResampler(44100, 48000, 1).Process(in), []int16{}...)
	rs := NewResampler(44100, 48000, 1)
	var chunked []int16
	for off := 0; off < len(in); off += 882 {
		end := off + 882
		if end > len(in) {
			end = len(in)
		}
		chunked = append(chunked, rs.Process(in[off:end])...)
	}
	chunked = append(chunked, rs.Flush()...)
	rsw := NewResampler(44100, 48000, 1)
	whole = append(rsw.Process(in), rsw.Flush()...)
	if len(chunked) != len(whole) {
		t.Fatalf("chunked=%d whole=%d", len(chunked), len(whole))
	}
	for i := range whole {
		if chunked[i] != whole[i] {
			t.Fatalf("chunk/whole mismatch at %d: %d != %d", i, chunked[i], whole[i])
		}
	}
}

func TestLayoutMapping(t *testing.T) {
	mono := []int16{100, -100, 200}
	st := MapChannels(mono, 1, LayoutStereo)
	if len(st) != 6 || st[0] != 100 || st[1] != 100 || st[2] != -100 {
		t.Fatalf("mono->stereo: %v", st)
	}
	back := MapChannels(st, 2, LayoutMono)
	for i, v := range mono {
		if back[i] != v {
			t.Fatalf("stereo->mono roundtrip: %v != %v", back, mono)
		}
	}
}

func TestLoudnessNormalize(t *testing.T) {
	pcm := sine(48000, 48000, 440)
	target := -16.0
	gain := GainFor(MeasureLUFS(pcm), target)
	out := append([]int16(nil), pcm...)
	ApplyGain(out, gain)
	if got := MeasureLUFS(out); math.Abs(got-target) > 0.05 {
		t.Fatalf("lufs=%.3f, want %.2f", got, target)
	}
	if Peak(out) > 1.0 {
		t.Fatalf("peak over 1.0: %f", Peak(out))
	}
}
