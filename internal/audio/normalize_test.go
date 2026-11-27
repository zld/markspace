package audio

import (
	"errors"
	"math"
	"testing"

	"example.com/assetpack/internal/fault"
)

func sine(f Format, frames int, freq float64, amp float64) PCM {
	p := Silence(f, frames)
	for ch := range p.Channels {
		for i := 0; i < frames; i++ {
			p.Channels[ch][i] = amp * math.Sin(2*math.Pi*freq*float64(i)/float64(f.Rate))
		}
	}
	return p
}

func TestFormatValidation(t *testing.T) {
	if err := (Format{Rate: 48000, Layout: LayoutStereo}).Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []Format{
		{Rate: 0, Layout: LayoutStereo},
		{Rate: 500000, Layout: LayoutStereo},
		{Rate: 48000, Layout: "quad"},
	}
	for _, c := range cases {
		var f *fault.Fault
		if err := c.Validate(); !errors.As(err, &f) || f.Kind != fault.KindInvalidArg {
			t.Fatalf("expected invalid-arg for %+v, got %v", c, err)
		}
	}
}

func TestTargetValidation(t *testing.T) {
	base := Target{Format: Format{Rate: 48000, Layout: LayoutStereo}, Loudness: -16}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Target{
		{Format: base.Format, Loudness: -100},
		{Format: base.Format, Loudness: math.NaN()},
		{Format: base.Format, Loudness: 0},
	}
	for _, b := range bad {
		if err := b.Validate(); err == nil {
			t.Fatalf("expected error for %+v", b)
		}
	}
}

func TestPCMValidate(t *testing.T) {
	p := Silence(Format{Rate: 48000, Layout: LayoutStereo}, 10)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Channels[1] = p.Channels[1][:5]
	if err := p.Validate(); err == nil {
		t.Fatal("ragged channels accepted")
	}
}

func TestResampleExactDuration(t *testing.T) {
	in := sine(Format{Rate: 44100, Layout: LayoutMono}, 44100, 1000, 0.5)
	out, err := Resample(in, 48000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames() != 48000 {
		t.Fatalf("frames %d", out.Frames())
	}
	if out.Format.Rate != 48000 {
		t.Fatalf("rate %d", out.Format.Rate)
	}
	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLayoutMixes(t *testing.T) {
	mono := sine(Format{Rate: 48000, Layout: LayoutMono}, 480, 440, 0.5)
	stereo, err := mapLayout(mono, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	if stereo.Frames() != mono.Frames() || len(stereo.Channels) != 2 {
		t.Fatal("mono->stereo shape wrong")
	}
	if stereo.Channels[0][10] != mono.Channels[0][10] || stereo.Channels[1][10] != mono.Channels[0][10] {
		t.Fatal("mono->stereo duplicated incorrectly")
	}
	back, err := mapLayout(stereo, LayoutMono)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(back.Channels[0][10]-mono.Channels[0][10]) > 1e-12 {
		t.Fatalf("stereo->mono not unity: %v vs %v", back.Channels[0][10], mono.Channels[0][10])
	}
	fiveOne := Silence(Format{Rate: 48000, Layout: Layout5_1}, 8)
	for c := range fiveOne.Channels {
		for i := range fiveOne.Channels[c] {
			fiveOne.Channels[c][i] = 0.1
		}
	}
	down, err := mapLayout(fiveOne, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	if len(down.Channels) != 2 || down.Channels[0][0] == 0 {
		t.Fatal("5.1->stereo produced silence")
	}
}

func TestLoudnessReferenceTone(t *testing.T) {
	// A 1 kHz sine of amplitude A has mean square A^2/2, so BS.1770 gives
	// -0.691 + 10*log10(A^2/2); for A = 0.1 that is about -23.3 LUFS.
	p := sine(Format{Rate: 48000, Layout: LayoutMono}, 48000*2, 1000, 0.1)
	got := Loudness(p)
	if math.IsInf(got, 0) || math.Abs(got-(-23.3)) > 1.0 {
		t.Fatalf("loudness %.3f LUFS, want ~-23.3", got)
	}
	// Digital silence yields negative infinity.
	if !math.IsInf(Loudness(Silence(Format{Rate: 48000, Layout: LayoutMono}, 4800)), -1) {
		t.Fatal("silence should measure -inf LUFS")
	}
}

func TestLoudnessGatingRejectsQuietPassages(t *testing.T) {
	f := Format{Rate: 48000, Layout: LayoutMono}
	p := Silence(f, 48000*4)
	loud := sine(f, 48000*2, 1000, 0.1)
	for i := range loud.Channels[0] {
		p.Channels[0][i] = loud.Channels[0][i]
	}
	quiet := sine(f, 48000*2, 1000, 0.0001)
	for i := range quiet.Channels[0] {
		p.Channels[0][48000*2+i] = quiet.Channels[0][i]
	}
	got := Loudness(p)
	if math.Abs(got-(-23.3)) > 1.2 {
		t.Fatalf("gated loudness %.3f, want near -23.3 (quiet half excluded)", got)
	}
}

func TestNormalizeReportAndGain(t *testing.T) {
	in := sine(Format{Rate: 44100, Layout: LayoutStereo}, 44100*2, 1000, 0.05)
	target := Target{Format: Format{Rate: 48000, Layout: LayoutStereo}, Loudness: -16}
	res, err := Normalize(in, target, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Report
	if !r.Valid || r.InRate != 44100 || r.OutRate != 48000 || r.OutFrames != 96000 {
		t.Fatalf("bad report: %+v", r)
	}
	if math.Abs(r.OutLoudnessLUFS-(-16)) > 0.5 {
		t.Fatalf("output loudness %.2f not at target", r.OutLoudnessLUFS)
	}
	if r.OutPeak > 0.999+1e-9 {
		t.Fatalf("peak %f exceeds ceiling", r.OutPeak)
	}
	if r.OutLoudnessLUFS <= r.InLoudnessLUFS {
		t.Fatalf("gain was not applied: in %.2f out %.2f", r.InLoudnessLUFS, r.OutLoudnessLUFS)
	}
}

func TestNormalizePeakLimiting(t *testing.T) {
	// A loud source pushed to a low ceiling must be peak limited.
	in := sine(Format{Rate: 48000, Layout: LayoutMono}, 48000, 1000, 0.9)
	target := Target{Format: Format{Rate: 48000, Layout: LayoutMono}, Loudness: -5}
	res, err := Normalize(in, target, nil, Options{PeakCeiling: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Report.PeakLimited {
		t.Fatal("expected peak limiting")
	}
	if res.Report.OutPeak > 0.7+1e-9 {
		t.Fatalf("peak %f above ceiling", res.Report.OutPeak)
	}
	if res.Report.OutLoudnessLUFS > -5 {
		t.Fatalf("loudness should not exceed target under limiting: %.2f", res.Report.OutLoudnessLUFS)
	}
}

func TestNormalizeGapPadsNotShortens(t *testing.T) {
	in := sine(Format{Rate: 48000, Layout: LayoutMono}, 48000, 1000, 0.1)
	gaps := []Gap{{Start: 24000, Length: 4800, Offset: 188 * 10}}
	clean, err := Normalize(in, Target{Format: in.Format, Loudness: -16}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	gapped, err := Normalize(in, Target{Format: in.Format, Loudness: -16}, gaps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if gapped.Report.OutFrames != clean.Report.OutFrames+4800 {
		t.Fatalf("gap did not extend the timeline: %d vs %d", gapped.Report.OutFrames, clean.Report.OutFrames)
	}
	if gapped.Report.GapSamples != 4800 || len(gapped.Report.Gaps) != 1 {
		t.Fatalf("gap report missing: %+v", gapped.Report)
	}
	for i := int64(24000); i < 28800; i++ {
		if gapped.PCM.Channels[0][i] != 0 {
			t.Fatalf("gap sample %d not silent: %f", i, gapped.PCM.Channels[0][i])
		}
	}
	// Material outside the gap is untouched relative to the clean render.
	if gapped.PCM.Channels[0][1] == 0 || gapped.PCM.Channels[0][100] == 0 {
		t.Fatal("non-gap material lost")
	}
}

func TestNormalizeGapLimitFails(t *testing.T) {
	in := sine(Format{Rate: 48000, Layout: LayoutMono}, 48000, 1000, 0.1)
	gaps := []Gap{{Start: 0, Length: 1000}}
	_, err := Normalize(in, Target{Format: in.Format, Loudness: -16}, gaps, Options{MaxGapSamples: 100})
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindAudioGap || f.Recoverable {
		t.Fatalf("expected hard audio-gap fault, got %v", err)
	}
	if f.Sample != 1000 {
		t.Fatalf("gap fault sample position %d", f.Sample)
	}
}

func TestNormalizeInvalidTarget(t *testing.T) {
	in := sine(Format{Rate: 48000, Layout: LayoutMono}, 100, 1000, 0.1)
	_, err := Normalize(in, Target{Format: Format{Rate: 48000, Layout: LayoutStereo}, Loudness: 50}, nil, Options{})
	if err == nil {
		t.Fatal("invalid target accepted")
	}
}

func TestSortGapsMerges(t *testing.T) {
	got := SortGaps([]Gap{
		{Start: 100, Length: 50},
		{Start: 10, Length: 20},
		{Start: 25, Length: 10},
		{Start: 120, Length: 40},
		{Start: 5, Length: 0},
	})
	if len(got) != 2 {
		t.Fatalf("merge failed: %+v", got)
	}
	if got[0].Start != 10 || got[0].Length != 25 {
		t.Fatalf("first merge wrong: %+v", got[0])
	}
	if got[1].Start != 100 || got[1].Length != 60 {
		t.Fatalf("second merge wrong: %+v", got[1])
	}
}

func TestReportString(t *testing.T) {
	r := Report{InRate: 44100, OutRate: 48000, InLayout: "stereo", OutLayout: "stereo",
		InFrames: 100, OutFrames: 109, InLoudnessLUFS: -20, OutLoudnessLUFS: -16}
	s := r.String()
	if s == "" {
		t.Fatal("empty report string")
	}
}
