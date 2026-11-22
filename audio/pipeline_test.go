package audio

import (
	"bytes"
	"math"
	"testing"

	"medpipe/timeline"
)

func TestWAVRoundTripWidths(t *testing.T) {
	orig := []float64{0, 0.25, -0.5, 0.99, -0.99, 0.123456, -0.234567, 0}
	for _, bits := range []uint16{16, 24, 32} {
		h := WAVHeader{SampleRate: 48000, Channels: 2, BitsPerSample: bits}
		var buf bytes.Buffer
		if err := WriteWAV(&buf, h, orig); err != nil {
			t.Fatalf("write %d-bit: %v", bits, err)
		}
		got, samples, err := ParseWAV(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("parse %d-bit: %v", bits, err)
		}
		if got.SampleRate != 48000 || got.Channels != 2 || got.BitsPerSample != bits {
			t.Fatalf("header mismatch %+v", got)
		}
		if len(samples) != len(orig) {
			t.Fatalf("%d-bit: got %d samples, want %d", bits, len(samples), len(orig))
		}
		tol := 1.0 / math.Pow(2, float64(bits-2))
		for i := range orig {
			if math.Abs(samples[i]-orig[i]) > tol {
				t.Fatalf("%d-bit sample %d: got %f want %f", bits, i, samples[i], orig[i])
			}
		}
	}
}

func TestWAVTruncationAndBadLength(t *testing.T) {
	h := WAVHeader{SampleRate: 48000, Channels: 1, BitsPerSample: 16}
	var buf bytes.Buffer
	_ = WriteWAV(&buf, h, []float64{0.1, 0.2, 0.3})
	raw := buf.Bytes()
	cuts := [][]byte{
		raw[:len(raw)-3],                        // truncated data payload
		raw[:11],                                // truncated header
		append([]byte("XXXX"), raw[4:]...),      // bad magic
	}
	for i, bad := range cuts {
		if _, _, err := ParseWAV(bytes.NewReader(bad)); err == nil {
			t.Fatalf("case %d: corrupt WAV must be rejected", i)
		} else if pe, ok := timeline.AsError(err); !ok ||
			(pe.Code != timeline.ErrCodeTruncatedWAV && pe.Code != timeline.ErrCodeIllegalBlockLength) {
			t.Fatalf("case %d: wrong error type %v", i, err)
		}
	}
	// Payload that does not tile the channel frame.
	if _, err := DecodePCM([]byte{0, 1, 2}, 16, 2); err == nil {
		t.Fatal("unaligned PCM payload must be rejected")
	}
	if _, err := DecodePCM([]byte{0, 1}, 8, 1); err == nil {
		t.Fatal("8-bit PCM is outside the supported set")
	}
}

func TestResamplerFrameCountAndState(t *testing.T) {
	cases := []struct {
		name       string
		in, out    uint32
		frames     int64
	}{
		{"48k_to_44.1k", 48000, 44100, 48000},
		{"44.1k_to_48k", 44100, 48000, 44100},
		{"upsample-2x", 24000, 48000, 24000},
		{"downsample-2x", 48000, 24000, 48000},
		{"passthrough", 48000, 48000, 12345},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One-shot run.
			r1, _ := NewResampler(tc.in, tc.out, 1)
			block := make([][]float64, 1)
			block[0] = make([]float64, tc.frames)
			for i := range block[0] {
				block[0][i] = math.Sin(float64(i) * 0.01)
			}
			o1, err := r1.Process(block)
			if err != nil {
				t.Fatal(err)
			}
			fl1 := r1.Flush()
			total1 := int64(len(o1[0]) + len(fl1[0]))
			theory := r1.ExpectedOutputs(tc.frames)
			if d := total1 - theory; d > 1 || d < -1 {
				t.Fatalf("frame count error %d exceeds one frame (got %d want %d)", d, total1, theory)
			}
			// Same stream delivered in small chunks must reach the same count.
			r2, _ := NewResampler(tc.in, tc.out, 1)
			var total2 int64
			for start := int64(0); start < tc.frames; start += 37 {
				end := start + 37
				if end > tc.frames {
					end = tc.frames
				}
				chunk := [][]float64{block[0][start:end]}
				o2, err := r2.Process(chunk)
				if err != nil {
					t.Fatal(err)
				}
				total2 += int64(len(o2[0]))
			}
			total2 += int64(len(r2.Flush()[0]))
			if total2 != total1 {
				t.Fatalf("chunked run produced %d frames, one-shot %d", total2, total1)
			}
		})
	}
}

func TestResamplerChannelMismatchRejected(t *testing.T) {
	r, _ := NewResampler(48000, 24000, 2)
	bad := [][]float64{{1, 2, 3}, {1, 2}}
	if _, err := r.Process(bad); err == nil {
		t.Fatal("unequal channel lengths must be rejected")
	}
	if _, err := r.Process([][]float64{{1}}); err == nil {
		t.Fatal("wrong channel count must be rejected")
	}
	good := [][]float64{{1, 2, 3}, {4, 5, 6}}
	if _, err := r.Process(good); err != nil {
		t.Fatal(err)
	}
	r.Flush()
	if _, err := r.Process(good); err == nil {
		t.Fatal("process after flush must be rejected")
	}
}

func TestLayoutNormalizationExplicit(t *testing.T) {
	// 5.1 fold-down: a signal only on Ls must appear in the stereo L output
	// and nowhere else, proving no channel is silently dropped.
	m, err := BuildMatrix(Layout51, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]float64, 6)
	frame[4] = 1.0 // Ls
	out, err := m.Apply(frame)
	if err != nil || out[0] == 0 || out[1] != 0 {
		t.Fatalf("Ls fold-down wrong: %v err=%v", out, err)
	}
	// LFE is attenuated but retained on both stereo channels.
	frame = make([]float64, 6)
	frame[3] = 1.0
	out, _ = m.Apply(frame)
	if out[0] != 0.25 || out[1] != 0.25 {
		t.Fatalf("LFE fold-down = %v, want [0.25 0.25]", out)
	}
	// Explicit swap of the alternative stereo ordering.
	swap, err := BuildMatrix(LayoutStereoAlt, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	out, _ = swap.Apply([]float64{7, 3})
	if out[0] != 3 || out[1] != 7 {
		t.Fatalf("stereo swap = %v", out)
	}
	if _, err := BuildMatrix("ambisonic-9", LayoutStereo); err == nil {
		t.Fatal("unknown layout must be rejected")
	}
	if _, err := LookupLayout("nope"); err == nil {
		t.Fatal("unknown layout lookup must fail")
	}
}

func TestLoudnessSilenceAndLimiter(t *testing.T) {
	silence := make([]float64, 4800) // 100 ms stereo silence
	out, rep, err := LimitLoudness(silence, 2, LayoutStereo, 48000, -23)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range out {
		if v != 0 {
			t.Fatal("silence must stay exactly silent")
		}
	}
	if rep.InputLUFS != SilenceLUFS || rep.OutputLUFS != SilenceLUFS {
		t.Fatalf("silence loudness = %f/%f", rep.InputLUFS, rep.OutputLUFS)
	}
	// Full-scale signal: even after attenuation toward the -23 LUFS target the
	// 4x oversampled true peak reaches the ceiling and the clamp engages.
	loud := make([]float64, 48000)
	for i := range loud {
		loud[i] = math.Sin(float64(i) * 0.05)
	}
	out, rep, err = LimitLoudness(loud, 2, LayoutStereo, 48000, -23)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ClippedSamples == 0 {
		t.Fatal("over-ceiling signal must report clipped samples")
	}
	if rep.OutputTruePeakDBTP > -1.0 {
		t.Fatalf("output true peak %f dBTP exceeds -1 dBTP", rep.OutputTruePeakDBTP)
	}
	for _, v := range out {
		if math.Abs(v) > math.Pow(10, -1.0/20)+1e-12 {
			t.Fatalf("sample %f breaks hard ceiling", v)
		}
	}
	if rep.InputLUFS == rep.OutputLUFS && rep.GainDB == 0 {
		t.Fatal("loudness normalization did not adjust the signal")
	}
}

func TestPipelineInvariantsEndToEnd(t *testing.T) {
	p, err := NewPipeline("trk1", 48000, 44100, Layout51, LayoutStereo, -23)
	if err != nil {
		t.Fatal(err)
	}
	const blocks = 8
	var totalIn int64
	var allOut []float64
	for b := 0; b < blocks; b++ {
		frames := 1000 + b*50
		totalIn += int64(frames)
		pcm := make([]byte, frames*6*2)
		for i := 0; i < frames*6; i++ {
			s := int16(4000 * math.Sin(float64(i+b)*0.02))
			pcm[i*2] = byte(s)
			pcm[i*2+1] = byte(s >> 8)
		}
		res, err := p.ProcessBlock(pcm, 16, 0)
		if err != nil {
			t.Fatalf("block %d: %v", b, err)
		}
		allOut = append(allOut, res.Samples...)
	}
	fl, err := p.Flush()
	if err != nil {
		t.Fatal(err)
	}
	allOut = append(allOut, fl.Samples...)
	st := p.Stats()
	if st.InputFrames != totalIn {
		t.Fatalf("input frames %d != submitted %d", st.InputFrames, totalIn)
	}
	theory := p.ExpectedOutputFrames(totalIn)
	if d := st.OutputFrames - theory; d > 1 || d < -1 {
		t.Fatalf("output frames %d differ from theory %d by %d", st.OutputFrames, theory, d)
	}
	if st.ResampledFrames != st.OutputFrames {
		t.Fatal("resampled and final frame counts must match")
	}
	if len(allOut) != int(st.OutputFrames)*2 {
		t.Fatalf("emitted samples %d != 2 * %d output frames", len(allOut), st.OutputFrames)
	}
}
