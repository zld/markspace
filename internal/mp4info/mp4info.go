// Package mp4info reuses go-mp4's box model to read an MP4's timeline and
// sample structure. It is read-only: the kernel derives timestamps and sample
// boundaries here rather than reimplementing the ISO base media file format.
package mp4info

import (
	"errors"
	"fmt"
	"os"

	"github.com/abema/go-mp4"
)

// ErrNoTrack means the MP4 contains no usable sample/timing boxes.
var ErrNoTrack = errors.New("mp4 track timing information missing")

// TimelineInfo is the reusable timing view of one MP4 track.
type TimelineInfo struct {
	Timescale   uint32 // mdhd timescale, ticks per second
	Duration    uint64 // mdhd duration in timescale units
	SampleCount uint32 // stsz sample count
	// SampleSizes holds every entry size when present; empty means a constant
	// sample size declared by stsz.SampleSize.
	SampleSizes  []uint32
	ConstantSize uint32
	// Deltas is the flattened per-sample decoding delta list from stts.
	Deltas []uint32
}

// DurationMS converts the track duration to milliseconds on its own timescale.
func (t TimelineInfo) DurationMS() int64 {
	if t.Timescale == 0 {
		return 0
	}
	return int64(t.Duration * 1000 / uint64(t.Timescale))
}

// SampleTimeline returns the cumulative decode timestamp (in timescale units)
// at which each sample starts, derived purely from the stts delta table.
func (t TimelineInfo) SampleTimeline() []uint64 {
	out := make([]uint64, 0, len(t.Deltas))
	var acc uint64
	for _, d := range t.Deltas {
		out = append(out, acc)
		acc += uint64(d)
	}
	return out
}

// Read opens an MP4 and extracts the first track's timing/sample structure.
func Read(path string) (*TimelineInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readFrom(f)
}

func readFrom(f *os.File) (*TimelineInfo, error) {
	mdhd, err := extractOne[*mp4.Mdhd](f, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()})
	if err != nil {
		return nil, err
	}
	stsz, err := extractOne[*mp4.Stsz](f, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz(),
	})
	if err != nil {
		return nil, err
	}
	stts, err := extractOne[*mp4.Stts](f, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts(),
	})
	if err != nil {
		return nil, err
	}
	info := &TimelineInfo{
		Timescale:    mdhd.Timescale,
		Duration:     mdhd.GetDuration(),
		SampleCount:  stsz.SampleCount,
		ConstantSize: stsz.SampleSize,
		SampleSizes:  append([]uint32(nil), stsz.EntrySize...),
	}
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			info.Deltas = append(info.Deltas, e.SampleDelta)
		}
	}
	if info.Timescale == 0 || info.SampleCount == 0 {
		return nil, fmt.Errorf("%w: timescale=%d samples=%d", ErrNoTrack, info.Timescale, info.SampleCount)
	}
	return info, nil
}

// extractOne is a typed wrapper around go-mp4's box extraction.
func extractOne[T mp4.IBox](f *os.File, path mp4.BoxPath) (T, error) {
	var zero T
	boxes, err := mp4.ExtractBoxWithPayload(f, nil, path)
	if err != nil {
		return zero, err
	}
	if len(boxes) == 0 {
		return zero, fmt.Errorf("%w: %s not found", ErrNoTrack, path)
	}
	payload, ok := boxes[0].Payload.(T)
	if !ok {
		return zero, fmt.Errorf("unexpected payload type for %s", path)
	}
	return payload, nil
}
