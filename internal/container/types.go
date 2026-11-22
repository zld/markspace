package container

import (
	"mediacore/internal/core"
)

// SttsRun is one time-to-sample run in track timescale units.
type SttsRun struct {
	Count uint32
	Delta uint32
}

// StscRun maps a run of chunks to samples per chunk.
type StscRun struct {
	FirstChunk       uint32
	SamplesPerChunk  uint32
	DescriptionIndex uint32
}

// AudioFormat describes the sample entry of an audio track.
type AudioFormat struct {
	Codec      string
	Channels   int
	SampleRate int
	SampleSize int
}

// TrackScan is the index metadata of one track as read from moov. It is raw:
// no sample has been validated against the file yet.
type TrackScan struct {
	ID        uint32
	Handler   string
	Timescale uint32
	Duration  uint64
	EditShift int64 // signed media time of the first edit, in timescale units
	Format    AudioFormat
	IsAudio   bool

	// DependsOn lists track ids this track declares a dependency on. The
	// dependency is read from an explicit "dep " child of udta; absent
	// metadata means no declared dependency, never an inferred one.
	DependsOn []uint32

	Stts      []SttsRun
	Stsc      []StscRun
	Sizes     []uint32 // expanded per-sample sizes
	FixedSize uint32   // non-zero when stsz declares a constant size
	Offsets   []int64  // chunk offsets in bytes
	Declared  int      // stsz sample count

	IndexComplete bool
	Note          string
	Warnings      []*core.Error
}

// SampleCount returns the number of sample sizes actually available.
func (t *TrackScan) SampleCount() int {
	if t.FixedSize != 0 {
		return t.Declared
	}
	return len(t.Sizes)
}
