// Package audio normalizes decoded PCM to a distribution target: sample rate,
// channel layout and integrated loudness.
//
// The package never shortens a timeline.  Gaps reported by the transport
// layer are padded with silence and counted; the output frame count is
// derived from the declared input duration, and any disagreement is a
// non-recoverable fault rather than a silent trim.
package audio

import (
	"math"

	"example.com/assetpack/internal/fault"
)

// Target is the requested distribution specification.
type Target struct {
	Format   Format
	Loudness float64 // integrated loudness target in LUFS
}

// Validate rejects impossible target parameters.
func (t Target) Validate() error {
	if err := t.Format.Validate(); err != nil {
		return err
	}
	if math.IsNaN(t.Loudness) || math.IsInf(t.Loudness, 0) {
		return fault.New(fault.StageAudio, fault.KindInvalidArg, "loudness target is not finite")
	}
	if t.Loudness < -70 || t.Loudness > -5 {
		return fault.New(fault.StageAudio, fault.KindInvalidArg,
			"loudness target %.1f LUFS outside -70..-5", t.Loudness)
	}
	return nil
}

// Result pairs the normalized PCM with its report.
type Result struct {
	PCM    PCM
	Report Report
}

// Options tunes normalization behaviour.
type Options struct {
	// MaxGapSamples fails the run when the total missing material exceeds
	// this many frames.  Zero means gaps are tolerated and padded.
	MaxGapSamples int64
	// PeakCeiling is the post-gain absolute sample ceiling.  Zero defaults
	// to 0.999.
	PeakCeiling float64
}

// Normalize converts in to the target format, applies channel mapping,
// resamples, and finally applies a loudness gain with peak protection.
func Normalize(in PCM, target Target, gaps []Gap, opts Options) (*Result, error) {
	rep := Report{
		InRate: in.Format.Rate, OutRate: target.Format.Rate,
		InLayout: string(in.Format.Layout), OutLayout: string(target.Format.Layout),
		InFrames: int64(in.Frames()),
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if err := in.Validate(); err != nil {
		rep.Valid = false
		return &Result{Report: rep}, err
	}
	rep.InPeak = in.Peak()
	rep.InLoudnessLUFS, rep.InSilent = finiteLoudness(Loudness(in))

	span, err := totalSpan(in.Frames(), in.Format.Rate, target.Format.Rate, gaps)
	if err != nil {
		return nil, err
	}
	if opts.MaxGapSamples > 0 && totalGap(gaps) > opts.MaxGapSamples {
		return nil, fault.New(fault.StageAudio, fault.KindAudioGap,
			"%d missing sample frames exceed the %d allowed", totalGap(gaps), opts.MaxGapSamples).
			AtSample(totalGap(gaps)).Recover(false)
	}

	mapped, err := mapLayout(in, target.Format.Layout)
	if err != nil {
		return nil, err
	}
	resampled, err := Resample(mapped, target.Format.Rate, span)
	if err != nil {
		return nil, err
	}
	applyGaps(resampled, gaps)

	result := &Result{}
	result.PCM = resampled
	result.Report = rep
	result.Report.OutFrames = int64(resampled.Frames())
	result.Report.Gaps = append([]Gap(nil), gaps...)
	result.Report.GapSamples = totalGap(gaps)

	loud := Loudness(resampled)
	ceiling := opts.PeakCeiling
	if ceiling <= 0 {
		ceiling = 0.999
	}
	gainDB := 0.0
	if !math.IsInf(loud, -1) && !math.IsNaN(loud) && loud > -70 {
		gainDB = target.Loudness - loud
	}
	gain := math.Pow(10, gainDB/20)
	if peak := resampled.Peak() * gain; peak > ceiling && peak > 0 {
		gain *= ceiling / peak
		gainDB = 20 * math.Log10(gain)
		result.Report.PeakLimited = true
	}
	applyGain(resampled, gain)
	result.Report.GainDB = round3(gainDB)
	result.Report.OutPeak = round6(resampled.Peak())
	result.Report.OutLoudnessLUFS, result.Report.OutSilent = finiteLoudness(Loudness(resampled))
	result.Report.Valid = true
	return result, nil
}

func totalGap(gaps []Gap) int64 {
	var n int64
	for _, g := range gaps {
		n += g.Length
	}
	return n
}

// totalSpan computes the output frame count from the input duration plus the
// declared gaps, so padding can never shorten the timeline.
func totalSpan(inFrames, inRate, outRate int, gaps []Gap) (int, error) {
	if inRate <= 0 || outRate <= 0 {
		return 0, fault.New(fault.StageAudio, fault.KindInvalidArg, "invalid rates %d -> %d", inRate, outRate)
	}
	var gapFrames int64
	for _, g := range gaps {
		gapFrames += g.Length
	}
	base := int64(inFrames) * int64(outRate) / int64(inRate)
	// Gaps are already expressed on the output clock.
	total := base + gapFrames
	if total > math.MaxInt32 {
		return 0, fault.New(fault.StageAudio, fault.KindInvalidArg, "output span %d frames is too large", total)
	}
	return int(total), nil
}

// applyGaps silences declared missing intervals on the output clock.  The
// surrounding material is left in place, so durations are preserved.
func applyGaps(p PCM, gaps []Gap) {
	for _, g := range gaps {
		for i := int64(0); i < g.Length; i++ {
			idx := g.Start + i
			if idx < 0 {
				continue
			}
			for _, c := range p.Channels {
				if int(idx) < len(c) {
					c[int(idx)] = 0
				}
			}
		}
	}
}

func applyGain(p PCM, gain float64) {
	for _, ch := range p.Channels {
		for i := range ch {
			ch[i] *= gain
		}
	}
}

// mapLayout converts the channel count to the target layout using the
// standard weighted down/up-mix coefficients.  Unknown mixes fail loudly.
func mapLayout(in PCM, target Layout) (PCM, error) {
	if in.Format.Layout == target {
		return in, nil
	}
	inCh, err := in.Format.Layout.Channels()
	if err != nil {
		return PCM{}, err
	}
	outCh, _ := target.Channels()
	out := Silence(Format{Rate: in.Format.Rate, Layout: target}, in.Frames())
	idx := func(names []string, name string) int {
		for i, n := range names {
			if n == name {
				return i
			}
		}
		return -1
	}
	switch target {
	case LayoutMono:
		switch in.Format.Layout {
		case LayoutStereo:
			for i := 0; i < in.Frames(); i++ {
				out.Channels[0][i] = 0.5 * (in.Channels[0][i] + in.Channels[1][i])
			}
		case Layout5_1:
			for i := 0; i < in.Frames(); i++ {
				out.Channels[0][i] = 0.5*in.Channels[idx(inCh, "L")][i] +
					0.5*in.Channels[idx(inCh, "R")][i] +
					0.707*in.Channels[idx(inCh, "C")][i] +
					0.5*in.Channels[idx(inCh, "Ls")][i] +
					0.5*in.Channels[idx(inCh, "Rs")][i]
			}
		}
	case LayoutStereo:
		switch in.Format.Layout {
		case LayoutMono:
			for i := 0; i < in.Frames(); i++ {
				out.Channels[0][i] = in.Channels[0][i]
				out.Channels[1][i] = in.Channels[0][i]
			}
		case Layout5_1:
			for i := 0; i < in.Frames(); i++ {
				l := in.Channels[idx(inCh, "L")][i]
				r := in.Channels[idx(inCh, "R")][i]
				c := in.Channels[idx(inCh, "C")][i]
				ls := in.Channels[idx(inCh, "Ls")][i]
				rs := in.Channels[idx(inCh, "Rs")][i]
				out.Channels[0][i] = l + 0.707*c + 0.707*ls
				out.Channels[1][i] = r + 0.707*c + 0.707*rs
			}
		}
	case Layout5_1:
		if in.Format.Layout == LayoutMono || in.Format.Layout == LayoutStereo {
			for i := 0; i < in.Frames(); i++ {
				var l, r float64
				if in.Format.Layout == LayoutMono {
					l, r = in.Channels[0][i], in.Channels[0][i]
				} else {
					l, r = in.Channels[0][i], in.Channels[1][i]
				}
				out.Channels[idx(outCh, "L")][i] = l
				out.Channels[idx(outCh, "R")][i] = r
				out.Channels[idx(outCh, "C")][i] = 0.707 * (l + r)
				out.Channels[idx(outCh, "Ls")][i] = 0.5 * l
				out.Channels[idx(outCh, "Rs")][i] = 0.5 * r
			}
		}
	}
	return out, nil
}

// Resample converts the sample rate with windowed linear interpolation and
// pads or trims to exactly span frames.  span is authoritative: it comes from
// the declared input duration, never from the resampled slice length.
func Resample(in PCM, outRate, span int) (PCM, error) {
	if in.Format.Rate <= 0 || outRate <= 0 {
		return PCM{}, fault.New(fault.StageAudio, fault.KindInvalidArg, "invalid resample rates")
	}
	if span < 0 {
		return PCM{}, fault.New(fault.StageAudio, fault.KindInvalidArg, "negative span %d", span)
	}
	if in.Format.Rate == outRate && in.Frames() == span {
		return in, nil
	}
	out := Silence(Format{Rate: outRate, Layout: in.Format.Layout}, span)
	inFrames := in.Frames()
	if inFrames == 0 {
		return out, nil
	}
	ratio := float64(in.Format.Rate) / float64(outRate)
	for ch := range in.Channels {
		src := in.Channels[ch]
		dst := out.Channels[ch]
		for i := 0; i < span; i++ {
			pos := float64(i) * ratio
			j := int(pos)
			frac := pos - float64(j)
			var a, b float64
			if j < len(src) {
				a = src[j]
			}
			if j+1 < len(src) {
				b = src[j+1]
			}
			dst[i] = a + (b-a)*frac
		}
	}
	return out, nil
}
