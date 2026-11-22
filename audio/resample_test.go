package audio

import (
	"math"
	"testing"
)

func genSine(n, rate int, freq float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		v := math.Sin(2 * math.Pi * freq * float64(i) / float64(rate))
		out[i] = int16(v * 20000)
	}
	return out
}

func TestResampleChunkedEqualsOneShotUp(t *testing.T) {
	in := genSine(4410, 44100, 440) // 100 ms
	one, err := ResampleAll(in, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := NewResampler(44100, 48000)
	var chunked []int16
	sizes := []int{1, 7, 64, 100, 257}
	start, si := 0, 0
	for start < len(in) {
		size := sizes[si%len(sizes)]
		si++
		end := start + size
		if end > len(in) {
			end = len(in)
		}
		chunked = append(chunked, r.Process(in[start:end])...)
		start = end
	}
	chunked = append(chunked, r.Flush()...)
	if len(one) != len(chunked) {
		t.Fatalf("length mismatch one=%d chunked=%d", len(one), len(chunked))
	}
	for i := range one {
		if one[i] != chunked[i] {
			t.Fatalf("sample %d differs: one=%d chunked=%d", i, one[i], chunked[i])
		}
	}
}

func TestResampleChunkedEqualsOneShotDown(t *testing.T) {
	in := genSine(4800, 48000, 440)
	one, err := ResampleAll(in, 48000, 44100)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := NewResampler(48000, 44100)
	var chunked []int16
	for start := 0; start < len(in); start += 37 {
		end := start + 37
		if end > len(in) {
			end = len(in)
		}
		chunked = append(chunked, r.Process(in[start:end])...)
	}
	chunked = append(chunked, r.Flush()...)
	if len(one) != len(chunked) {
		t.Fatalf("length mismatch one=%d chunked=%d", len(one), len(chunked))
	}
	for i := range one {
		if one[i] != chunked[i] {
			t.Fatalf("sample %d differs: one=%d chunked=%d", i, one[i], chunked[i])
		}
	}
}

func TestResampleFrameCountConservation(t *testing.T) {
	// output count must approximate the rate ratio within one frame
	in := genSine(44100, 44100, 1000)
	out, err := ResampleAll(in, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	ideal := int64(len(in)) * 48000 / 44100
	if d := int64(len(out)) - ideal; d < -1 || d > 1 {
		t.Fatalf("output frames %d too far from ideal %d", len(out), ideal)
	}
}

func TestResampleCheckpointResume(t *testing.T) {
	in := genSine(2000, 44100, 300)
	r, _ := NewResampler(44100, 48000)
	var direct []int16
	for start := 0; start < len(in); start += 128 {
		end := start + 128
		if end > len(in) {
			end = len(in)
		}
		direct = append(direct, r.Process(in[start:end])...)
	}

	// replay the same stream but restore from a checkpoint at the midpoint
	r2, _ := NewResampler(44100, 48000)
	var resumed []int16
	mid := 1000
	for start := 0; start < mid; start += 128 {
		end := start + 128
		if end > mid {
			end = mid
		}
		resumed = append(resumed, r2.Process(in[start:end])...)
	}
	snap := r2.Snapshot()
	r3 := RestoreResampler(snap)
	for start := mid; start < len(in); start += 128 {
		end := start + 128
		if end > len(in) {
			end = len(in)
		}
		resumed = append(resumed, r3.Process(in[start:end])...)
	}
	if len(direct) != len(resumed) {
		t.Fatalf("resume length %d != direct %d", len(resumed), len(direct))
	}
	for i := range direct {
		if direct[i] != resumed[i] {
			t.Fatalf("resume sample %d differs: %d vs %d", i, resumed[i], direct[i])
		}
	}
}

func TestResampleRejectsBadRates(t *testing.T) {
	if _, err := NewResampler(0, 48000); err == nil {
		t.Fatal("zero rate accepted")
	}
	if _, err := NewResampler(48000, -1); err == nil {
		t.Fatal("negative rate accepted")
	}
}

func TestResampleInterpolationRoundingRule(t *testing.T) {
	// midpoint between 1000 and 1002 at weight den/2 must round to 1001
	if got := interpolate(1000, 1002, 4, 8); got != 1001 {
		t.Fatalf("midpoint interpolation = %d", got)
	}
	// negative midpoint rounds half away from zero
	if got := interpolate(-1000, -1002, 4, 8); got != -1001 {
		t.Fatalf("negative midpoint = %d", got)
	}
}
