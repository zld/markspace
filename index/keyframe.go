// Package index builds and verifies keyframe indexes against actual samples.
package index

import (
	"bytes"
	"fmt"

	"example.com/media-kernel/media"
)

// IndexVersion is written into checkpoints and manifests when index semantics change.
const IndexVersion = "keyidx-v1"

// KeyMarker identifies a generated intra/key sample in fixture mdat bytes.
var KeyMarker = []byte("KFRM")

// DeltaMarker identifies a generated inter/delta sample.
var DeltaMarker = []byte("PFRM")

// Sample is the minimal sample-table information needed for indexing.
type Sample struct {
	Number        uint32
	StartTicks    int64
	DurationTicks uint32
	Size          uint32
	ByteStart     int64
}

// ByteRange returns the sample's half-open file byte range.
func (s Sample) ByteRange() media.ByteRange {
	return media.ByteRange{Start: s.ByteStart, End: s.ByteStart + int64(s.Size)}
}

// Keyframe is one indexed boundary after verification against sample bytes.
type Keyframe struct {
	SampleNumber  uint32          `json:"sample_number"`
	Time          media.Timestamp `json:"time"`
	IntervalTicks int64           `json:"interval_ticks,omitempty"`
	ByteRange     media.ByteRange `json:"byte_range"`
	Confirmed     bool            `json:"confirmed"`
	Marker        string          `json:"marker,omitempty"`
	Error         *media.Error    `json:"error,omitempty"`
}

// Index is a verified keyframe index.
type Index struct {
	Version      string         `json:"version"`
	Timebase     media.Timebase `json:"timebase"`
	Present      bool           `json:"present"`
	MissingIndex bool           `json:"missing_index,omitempty"`
	Keyframes    []Keyframe     `json:"keyframes"`
	IndexedCount int            `json:"indexed_count"`
	HitCount     int            `json:"hit_count"`
	Errors       []*media.Error `json:"errors,omitempty"`
}

// HitRate returns confirmed keyframes divided by stss/index entries.
func (idx *Index) HitRate() float64 {
	if idx == nil || idx.IndexedCount == 0 {
		return 0
	}
	return float64(idx.HitCount) / float64(idx.IndexedCount)
}

// ConfirmedBoundaries returns only keyframes whose bytes were verified.
func (idx *Index) ConfirmedBoundaries() []Keyframe {
	out := make([]Keyframe, 0, len(idx.Keyframes))
	for _, kf := range idx.Keyframes {
		if kf.Confirmed && kf.Error == nil {
			out = append(out, kf)
		}
	}
	return out
}

// ReadBytes reads a half-open byte range from an MP4/file abstraction.
type ReadBytes func(r media.ByteRange) ([]byte, error)

// BuildFromSamples verifies stss entries against actual sample markers. A
// missing stss list is an index problem; an stss entry whose sample is absent
// or does not contain a keyframe marker is corrupt/mismatched media data.
func BuildFromSamples(tb media.Timebase, syncSamples []uint32, samples []Sample, read ReadBytes) *Index {
	idx := &Index{Version: IndexVersion, Timebase: tb}
	if err := tb.Validate(); err != nil {
		e := err.(*media.Error)
		idx.Errors = append(idx.Errors, e)
		return idx
	}
	if syncSamples == nil {
		idx.MissingIndex = true
		e := media.NewError("index.keyframe", media.CodeIndexMissing,
			"sync sample table (stss) is missing", nil)
		idx.Errors = append(idx.Errors, e)
		return idx
	}

	idx.Present = true
	idx.IndexedCount = len(syncSamples)
	byNumber := make(map[uint32]Sample, len(samples))
	for _, s := range samples {
		byNumber[s.Number] = s
	}

	for i, number := range syncSamples {
		kf := Keyframe{SampleNumber: number}
		sample, ok := byNumber[number]
		if !ok {
			e := media.NewError("index.keyframe", media.CodeIndexMismatch,
				fmt.Sprintf("stss references sample %d absent from sample tables", number), nil)
			kf.Error = e
			idx.Errors = append(idx.Errors, e)
			idx.Keyframes = append(idx.Keyframes, kf)
			continue
		}

		kf.Time = media.Timestamp{Ticks: sample.StartTicks, Timebase: tb}
		kf.ByteRange = sample.ByteRange()
		if i+1 < len(syncSamples) {
			if next, ok := byNumber[syncSamples[i+1]]; ok {
				kf.IntervalTicks = next.StartTicks - sample.StartTicks
			}
		}

		if read != nil {
			data, err := read(kf.ByteRange)
			if err != nil {
				e := media.NewError("index.keyframe", media.CodeKeyframeCorrupt,
					fmt.Sprintf("cannot read sample %d bytes", number), err).
					WithByteRange(kf.ByteRange.Start, kf.ByteRange.End).
					WithRange(&media.TimeRange{Start: kf.Time, End: kf.Time})
				kf.Error = e
				idx.Errors = append(idx.Errors, e)
				idx.Keyframes = append(idx.Keyframes, kf)
				continue
			}
			if len(data) < len(KeyMarker) || !bytes.Equal(data[:len(KeyMarker)], KeyMarker) {
				marker := "short"
				if len(data) >= 4 {
					marker = fmt.Sprintf("%q", string(data[:4]))
				}
				e := media.NewError("index.keyframe", media.CodeKeyframeCorrupt,
					fmt.Sprintf("stss sample %d is indexed as keyframe but marker is %s", number, marker), nil).
					WithByteRange(kf.ByteRange.Start, kf.ByteRange.End).
					WithRange(&media.TimeRange{Start: kf.Time, End: kf.Time})
				kf.Marker = marker
				kf.Error = e
				idx.Errors = append(idx.Errors, e)
				idx.Keyframes = append(idx.Keyframes, kf)
				continue
			}
			kf.Marker = string(KeyMarker)
		}

		kf.Confirmed = true
		idx.HitCount++
		idx.Keyframes = append(idx.Keyframes, kf)
	}
	return idx
}
