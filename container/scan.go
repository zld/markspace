package container

import (
	mp4 "github.com/abema/go-mp4"

	"mediarc/model"
)

// Divergence describes a place where the index and the file disagree.
type Divergence struct {
	// Offset is the file position the index entry pointed at, or -1.
	Offset int64
	// Detail explains the disagreement.
	Detail string
	// Err is the stable error.
	Err *model.Error
}

// ScanResult is the recovered view of one file.
type ScanResult struct {
	// Size is the file length in bytes.
	Size int64
	// Tracks are the recovered tracks, in file order.
	Tracks []Track
	// Boxes lists top-level boxes that were readable, in file order.
	Boxes []mp4.BoxInfo
	// Divergences records every index/file disagreement in offset order.
	Divergences []Divergence
	// FirstUnreadable is the first byte offset that could not be parsed, or
	// -1 when the whole file was readable.
	FirstUnreadable int64
	// RecoveredSamples counts samples recovered by boundary scanning.
	RecoveredSamples int
	// RecoveredBytes counts bytes covered by boundary-recovered samples.
	RecoveredBytes int64
	// IndexComplete is false when the sample tables were missing, truncated
	// or inconsistent with the file.
	IndexComplete bool
}

// RecoveredWithinFile reports whether the recorded truncation offset is a real
// position inside the input, as the acceptance criteria require.
func (r ScanResult) RecoveredWithinFile() bool {
	return r.FirstUnreadable >= 0 && r.FirstUnreadable <= r.Size
}

// SampleCount returns the total number of samples in the result, including
// samples recovered by boundary scanning.
func (r ScanResult) SampleCount() int {
	total := 0
	for _, track := range r.Tracks {
		total += len(track.Samples)
	}
	return total
}

// IndexedSampleCount returns the number of samples that came from the index,
// excluding synthetic boundary-recovered ones.
func (r ScanResult) IndexedSampleCount() int {
	total := 0
	for _, track := range r.Tracks {
		for _, sample := range track.Samples {
			if !sample.Recovered {
				total++
			}
		}
	}
	return total
}

// ScanOptions tunes the scan.
type ScanOptions struct {
	// MaxSamplesPerTrack caps per-track work so a corrupt index cannot drive
	// unbounded memory. Zero means DefaultMaxSamplesPerTrack.
	MaxSamplesPerTrack int
	// BoundarySampleDurationNS is the timeline length assigned to each
	// boundary-recovered sample. Zero means 40 ms.
	BoundarySampleDurationNS int64
	// BoundarySampleSize is the byte length assigned to each boundary-recovered
	// sample. Zero means 512, fine enough to pick up the unindexed tail of a
	// truncated file.
	BoundarySampleSize int64
}

// Defaults for ScanOptions.
const (
	DefaultMaxSamplesPerTrack     = 100_000
	DefaultBoundarySampleDuration = 40 * 1_000_000
	DefaultBoundarySampleSize     = 512
)

func (o ScanOptions) withDefaults() ScanOptions {
	if o.MaxSamplesPerTrack <= 0 {
		o.MaxSamplesPerTrack = DefaultMaxSamplesPerTrack
	}
	if o.BoundarySampleDurationNS <= 0 {
		o.BoundarySampleDurationNS = DefaultBoundarySampleDuration
	}
	if o.BoundarySampleSize <= 0 {
		o.BoundarySampleSize = DefaultBoundarySampleSize
	}
	return o
}

// ScanFile scans the file at path.
