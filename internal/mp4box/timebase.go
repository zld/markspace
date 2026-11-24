// Package mp4box adapts MP4 container time bases to the pipeline's shared
// 90 kHz clock. It uses github.com/abema/go-mp4 for box parsing and writing
// and never trusts container timing blindly: every timescale is validated
// against the clock package before use.
package mp4box

import (
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediapipe/internal/clock"
	"mediapipe/internal/model"
)

// TimeBase carries the timing information extracted from mvhd/mdhd boxes.
type TimeBase struct {
	MovieTimescale uint32
	MovieDuration  uint64
	TrackTimescale uint32
	TrackDuration  uint64
}

// Parse reads an MP4 stream and returns its movie and first-track time base.
func Parse(r io.ReadSeeker) (*TimeBase, error) {
	tb := &TimeBase{}
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMvhd():
			payload, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mvhd, ok := payload.(*mp4.Mvhd)
			if !ok {
				return nil, fmt.Errorf("mp4box: unexpected mvhd payload %T", payload)
			}
			tb.MovieTimescale = mvhd.Timescale
			if mvhd.FullBox.Version == 1 {
				tb.MovieDuration = mvhd.DurationV1
			} else {
				tb.MovieDuration = uint64(mvhd.DurationV0)
			}
		case mp4.BoxTypeMdhd():
			payload, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mdhd, ok := payload.(*mp4.Mdhd)
			if !ok {
				return nil, fmt.Errorf("mp4box: unexpected mdhd payload %T", payload)
			}
			// First mdhd wins; tracks are handled separately by callers that
			// need every track's time base.
			if tb.TrackTimescale == 0 {
				tb.TrackTimescale = mdhd.Timescale
				if mdhd.FullBox.Version == 1 {
					tb.TrackDuration = mdhd.DurationV1
				} else {
					tb.TrackDuration = uint64(mdhd.DurationV0)
				}
			}
		}
		return nil, nil // descend into child boxes
	})
	if err != nil {
		return nil, model.NewError(model.ErrIO, "", 0, 0, "mp4 parse: "+err.Error())
	}
	if tb.MovieTimescale == 0 && tb.TrackTimescale == 0 {
		return nil, model.NewError(model.ErrInvalidConfig, "", 0, 0,
			"mp4 contains neither mvhd nor mdhd time base")
	}
	return tb, nil
}

// To90k maps a duration expressed in an MP4 timescale onto the shared clock.
func To90k(units int64, timescale uint32) (clock.Ticks, error) {
	if timescale == 0 {
		return 0, model.NewError(model.ErrInvalidConfig, "", 0, 0, "zero mp4 timescale")
	}
	return clock.FromSeconds(units, int64(timescale)), nil
}

// From90k converts shared-clock ticks back into a container timescale.
func From90k(t clock.Ticks, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, model.NewError(model.ErrInvalidConfig, "", 0, 0, "zero mp4 timescale")
	}
	return clock.ToRate(t, int64(timescale)), nil
}

// WriteMdhd serializes an mdhd box with the given timescale and duration so
// downstream tooling receives a container whose time base matches the clock.
func WriteMdhd(w io.WriteSeeker, timescale uint32, duration uint64) error {
	mw := mp4.NewWriter(w)
	bi, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()})
	if err != nil {
		return err
	}
	box := &mp4.Mdhd{
		Timescale:  timescale,
		DurationV0: uint32(duration),
	}
	if _, err := mp4.Marshal(mw, box, mp4.Context{}); err != nil {
		return err
	}
	_, err = mw.EndBox()
	_ = bi
	return err
}

// RoundTrip parses a written mdhd back and reports its timescale, used by
// tests to prove the written structure is valid MP4.
func RoundTrip(timescale uint32, duration uint64) (*TimeBase, error) {
	// The box writer needs a real seekable stream to backfill the size.
	f, err := os.CreateTemp("", "mp4box-*.mp4")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := WriteMdhd(f, timescale, duration); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return Parse(f)
}
