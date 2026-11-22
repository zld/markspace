package audio

import (
	"math"
	"testing"
)

// tone builds an interleaved sine buffer with a known frequency.
func tone(l Layout, frames, rate int, hz, amp float64) *Buffer {
	b, err := NewBuffer(l, frames)
	if err != nil {
		panic(err)
	}
	ch := l.NumChannels()
	for i := 0; i < frames; i++ {
		v := amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate))
		for c := 0; c < ch; c++ {
			b.Data[i*ch+c] = v
		}
	}
	return b
}

func TestResampleProducesExpectedFrameCount(t *testing.T) {
	src := tone(Stereo, 4410, 44100, 1000, 0.5)
	out, stats, err := NewResampler().ResampleAt(src, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames != 4800 {
		t.Fatalf("frames = %d, want 4800", out.Frames)
	}
	if stats.OutputFrames != 4800 || stats.InputFrames != 4410 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.Resets != 1 {
		t.Fatalf("resets = %d, want 1", stats.Resets)
	}
}

func TestResamplePreservesToneAmplitudeAndFrequency(t *testing.T) {
	// 1 kHz at 44.1k -> 48k. Measure the output amplitude away from edges.
	src := tone(Stereo, 44100, 44100, 1000, 0.5)
	out, _, err := NewResampler().ResampleAt(src, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	const guard = 200
	var peak float64
	for i := guard; i < out.Frames-guard; i++ {
		v := math.Abs(out.Frame(i)[0])
		if v > peak {
			peak = v
		}
	}
	if math.Abs(peak-0.5) > 0.05 {
		t.Fatalf("peak %0.4f deviates from 0.5 by more than 10%%", peak)
	}
	// Count zero crossings to confirm the frequency is preserved: 1 kHz over
	// ~1 s should be ~2000 crossings.
	cross := 0
	prev := out.Frame(guard)[0]
	for i := guard + 1; i < out.Frames-guard; i++ {
		v := out.Frame(i)[0]
		if (prev < 0 && v >= 0) || (prev >= 0 && v < 0) {
			cross++
		}
		prev = v
	}
	want := 2 * 1000 * (out.Frames - 2*guard) / 48000
	if math.Abs(float64(cross-want)) > float64(want)*0.05 {
		t.Fatalf("zero crossings %d, want about %d", cross, want)
	}
}

func TestResampleTailOfOddInputIsFlushed(t *testing.T) {
	// 1023 frames is an odd tail for 44.1k -> 48k.
	src := tone(Stereo, 1023, 44100, 440, 0.4)
	out, stats, err := NewResampler().ResampleAt(src, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	want := int(math.Round(float64(1023) * 48000 / 44100))
	if out.Frames != want {
		t.Fatalf("frames = %d, want %d", out.Frames, want)
	}
	if stats.TailFrames != 1 {
		t.Fatalf("tail frames = %d", stats.TailFrames)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("output invalid: %v", err)
	}
}

func TestResampleUpsampleDownsampleRoundTripKeepsLength(t *testing.T) {
	src := tone(Stereo, 48000, 48000, 440, 0.3)
	up, _, err := NewResampler().ResampleAt(src, 48000, 96000)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := NewResampler().ResampleAt(up, 96000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if back.Frames != src.Frames {
		t.Fatalf("round trip frames = %d, want %d", back.Frames, src.Frames)
	}
	if err := back.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResamplePassthroughIsExact(t *testing.T) {
	src := tone(Stereo, 1000, 48000, 440, 0.5)
	out, stats, err := NewResampler().ResampleAt(src, 48000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames != 1000 || stats.Resets != 0 {
		t.Fatalf("passthrough should not reset or change length: %+v", stats)
	}
	for i := range src.Data {
		if src.Data[i] != out.Data[i] {
			t.Fatalf("passthrough altered sample %d", i)
		}
	}
}

func TestResampleRejectsBadInput(t *testing.T) {
	src := tone(Stereo, 10, 48000, 440, 0.5)
	if _, _, err := NewResampler().ResampleAt(src, 0, 48000); err == nil {
		t.Fatal("zero input rate accepted")
	}
	bad := &Buffer{Layout: Stereo, Frames: 4, Data: make([]float64, 4)}
	if _, _, err := NewResampler().ResampleAt(bad, 48000, 96000); err == nil {
		t.Fatal("inconsistent buffer length accepted")
	}
	nan := tone(Stereo, 4, 48000, 440, 0.5)
	nan.Data[1] = math.NaN()
	if _, _, err := NewResampler().ResampleAt(nan, 48000, 96000); err == nil {
		t.Fatal("NaN input accepted")
	}
}

func TestConvertLayoutMonoToStereoAndBack(t *testing.T) {
	m := tone(Mono, 100, 48000, 440, 0.5)
	st, err := ConvertLayout(m, Stereo)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < st.Frames; i++ {
		if st.Frame(i)[0] != st.Frame(i)[1] {
			t.Fatalf("mono to stereo channels differ at %d", i)
		}
	}
	back, err := ConvertLayout(st, Mono)
	if err != nil {
		t.Fatal(err)
	}
	// The documented dual-mono upmix plus the 0.5+0.5 stereo downmix restores
	// the original level exactly.
	for i := 0; i < m.Frames; i++ {
		if math.Abs(back.Frame(i)[0]-m.Frame(i)[0]) > 1e-9 {
			t.Fatalf("round trip level changed at %d: %f vs %f", i, back.Frame(i)[0], m.Frame(i)[0])
		}
	}
}

func TestConvertLayoutPermutesByPosition(t *testing.T) {
	// Build a 4-channel buffer where each channel has a distinct DC value,
	// then reorder to a layout with swapped back channels.
	quad := Layout{Name: "quad-swapped", Channels: []Channel{ChFrontLeft, ChFrontRight, ChBackRight, ChBackLeft}}
	src, _ := NewBuffer(Quad, 1)
	src.Data = []float64{1, 2, 3, 4}
	out, err := ConvertLayout(src, quad)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{1, 2, 4, 3}
	for i, w := range want {
		if out.Data[i] != w {
			t.Fatalf("permute[%d] = %f, want %f", i, out.Data[i], w)
		}
	}
}

func TestConvertLayoutRejectsUnknownOrder(t *testing.T) {
	// A three-channel buffer is LCR; asking for 5.1 must fail rather than
	// inventing center/LFE/back content.
	src := tone(LCR, 10, 48000, 440, 0.2)
	if _, err := ConvertLayout(src, Surround51); err == nil {
		t.Fatal("unmappable layout conversion accepted")
	}
	if _, err := LayoutForChannels(6); err == nil {
		t.Fatal("ambiguous 6-channel layout accepted")
	}
	if _, err := ParseLayout("7.1-weird"); err == nil {
		t.Fatal("unknown layout name accepted")
	}
}

func TestConvertLayoutNeverFillsMissingChannelWithSilence(t *testing.T) {
	// 5.1-side to 5.1 needs back channels the source does not carry. Both
	// have six channels and the same positions except BL/BR vs SL/SR, so this
	// must be rejected, not silently zero-filled.
	src := tone(Surround51Side, 10, 48000, 440, 0.2)
	if _, err := ConvertLayout(src, Surround51); err == nil {
		t.Fatal("missing back channels were silently filled")
	}
}

func TestUpmixUsesDocumentedWeights(t *testing.T) {
	// Stereo with distinct channel DC values, expanded to 5.1.
	src, _ := NewBuffer(Stereo, 1)
	src.Data = []float64{0.8, 0.2}
	out, err := ConvertLayout(src, Surround51)
	if err != nil {
		t.Fatal(err)
	}
	const fold = 0.707
	want := []float64{
		0.8,        // FL
		0.2,        // FR
		fold * 1.0, // FC
		0.0,        // LFE: never synthesized
		fold * 0.8, // BL
		fold * 0.2, // BR
	}
	for i, w := range want {
		if math.Abs(out.Data[i]-w) > 1e-9 {
			t.Fatalf("upmix[%d] = %f, want %f", i, out.Data[i], w)
		}
	}
	if out.Layout.NumChannels() != 6 {
		t.Fatalf("layout = %s", out.Layout.Name)
	}
}

func TestUpmixMonoToSurroundIsFoldedNotSilent(t *testing.T) {
	src, _ := NewBuffer(Mono, 2)
	src.Data = []float64{0.5, -0.5}
	out, err := ConvertLayout(src, Quad)
	if err != nil {
		t.Fatal(err)
	}
	// FL, FR, BL, BR all carry the mono signal at documented weights.
	for f := 0; f < 2; f++ {
		fr := out.Frame(f)
		if fr[0] != src.Data[f] || fr[1] != src.Data[f] {
			t.Fatalf("front pair not dual-mono at frame %d: %v", f, fr)
		}
		if math.Abs(fr[2]-0.707*src.Data[f]) > 1e-9 {
			t.Fatalf("back left weight wrong at frame %d: %v", f, fr)
		}
	}
}
