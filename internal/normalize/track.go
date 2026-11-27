// Package normalize turns validated raw bitstream units into one canonical
// track model with an explicit integer timescale. All timing is derived with
// the exact rational helpers in internal/model; floating point is never used
// to accumulate time.
package normalize

import (
	"mediad/internal/ingest"
	"mediad/internal/model"
)

// Sample is one timed sample in the normalized track.
type Sample struct {
	// Index is the zero-based sample number.
	Index int
	// Offset/Length locate the sample bytes inside the mdat payload.
	Offset int64
	Length int64
	// IsKey mirrors the access-unit random-access flag.
	IsKey bool
	// PTSTicks is the presentation timestamp in track ticks.
	PTSTicks int64
	// PTSNS is the exact integer-nanosecond presentation timestamp.
	PTSNS int64
}

// Track is the canonical, codec-agnostic track model.
type Track struct {
	Codec     string
	Width     uint16
	Height    uint16
	Timescale uint32
	FPS       model.Rational
	Samples   []Sample
	// DurationTicks is the duration of the full sample sequence in ticks.
	DurationTicks int64
	// StartNS/EndNS bracket the presentation window in integer nanoseconds.
	StartNS int64
	EndNS   int64
	// MaxAlignmentNS is the largest per-sample tick rounding error.
	MaxAlignmentNS int64
}

// Config controls normalization.
type Config struct {
	// Width/Height are the display dimensions reported by the source header.
	Width, Height uint16
}

// chooseTimescale picks an explicit integer timescale under which every frame
// boundary lands on an exact tick. Using the frame-rate numerator makes one
// frame span fps.Den ticks (e.g. 1001 ticks at 30000 tps for NTSC), which keeps
// the alignment error at zero for every sample.
func chooseTimescale(fps model.Rational) (uint32, error) {
	if err := fps.Validate(false); err != nil {
		return 0, err
	}
	if fps.Num > (1<<32)-1 {
		return 0, model.New(model.ErrUnrepresentableTime, "", -1,
			"frame-rate numerator %d does not fit uint32 timescale", fps.Num)
	}
	return uint32(fps.Num), nil
}

// Build converts scanned access units into the canonical track. Units are
// expected in stream order; their offsets are reassigned sequentially as mdat
// sample offsets, so normalization never trusts raw-stream placement beyond
// what the scanner validated.
func Build(codec string, units []ingest.AccessUnit, fps model.Rational, cfg Config) (*Track, error) {
	if !ingest.IsSupportedCodec(codec) {
		return nil, model.New(model.ErrUnknownCodec, codec, -1, "cannot normalize codec %q", codec)
	}
	if len(units) == 0 {
		return nil, model.New(model.ErrTruncatedUnit, codec, -1, "no access units to normalize")
	}
	timescale, err := chooseTimescale(fps)
	if err != nil {
		return nil, err
	}
	// Explicit zero time-base rejection even if a caller bypasses helpers.
	if timescale == 0 {
		return nil, model.New(model.ErrZeroTimebase, codec, -1, "normalized track timescale is zero")
	}

	tr := &Track{
		Codec:     codec,
		Width:     cfg.Width,
		Height:    cfg.Height,
		Timescale: timescale,
		FPS:       fps,
		Samples:   make([]Sample, 0, len(units)),
	}

	var dataOffset int64
	for i, u := range units {
		ptsTicks, err := model.FramePTSTicks(int64(i), timescale, fps)
		if err != nil {
			return nil, err
		}
		ptsNS, err := model.FramePTSNS(int64(i), fps)
		if err != nil {
			return nil, err
		}
		alignNS, err := model.AlignmentErrorNS(int64(i), timescale, fps)
		if err != nil {
			return nil, err
		}
		if alignNS > tr.MaxAlignmentNS {
			tr.MaxAlignmentNS = alignNS
		}
		tr.Samples = append(tr.Samples, Sample{
			Index:    i,
			Offset:   dataOffset,
			Length:   u.Length,
			IsKey:    u.IsKey,
			PTSTicks: ptsTicks,
			PTSNS:    ptsNS,
		})
		dataOffset, err = model.CheckedAdd(dataOffset, u.Length)
		if err != nil {
			return nil, err
		}
	}

	// Duration: one more frame interval after the final sample start.
	last := len(tr.Samples) - 1
	frameDur, err := model.FramePTSTicks(int64(last+1), timescale, fps)
	if err != nil {
		return nil, err
	}
	tr.DurationTicks = frameDur
	tr.StartNS = tr.Samples[0].PTSNS
	tr.EndNS, err = model.FramePTSNS(int64(last+1), fps)
	if err != nil {
		return nil, err
	}
	return tr, nil
}

// KeySampleNumbers returns 1-based sample numbers of random-access samples,
// ready for an stss box.
func (t *Track) KeySampleNumbers() []uint32 {
	out := make([]uint32, 0)
	for _, s := range t.Samples {
		if s.IsKey {
			out = append(out, uint32(s.Index+1))
		}
	}
	return out
}
