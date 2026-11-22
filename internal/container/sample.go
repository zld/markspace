// Package container parses MP4/MOV sample tables with github.com/abema/go-mp4
// and emits timestamped sample events on the canonical nanosecond scale.
package container

import (
	"io"

	"mediatimeline/internal/clock"
)

// SampleEvent is one normalized container sample. Original track tick values
// are retained alongside canonical nanoseconds so downstream stages can locate
// the source position without inverting the rational scaling.
type SampleEvent struct {
	TrackID      uint32
	SampleIndex  int64 // zero-based, in track sample-table order
	DTSNanos     uint64
	PTSNanos     uint64
	DTSRaw       uint64 // original DTS in track timescale ticks
	PTSRaw       uint64 // original PTS (CTTS shift applied), track ticks
	DurationRaw  uint64 // sample duration in track ticks
	Timescale    uint32
	FileOffset   int64 // absolute byte offset of the sample payload
	PayloadSize  int64
	PayloadCRC32 uint32 // CRC over the payload bytes, checked by the pipeline
}

// EventOrder is the stable replay sort key: time first, then track, then the
// original sample index. Equal keys keep insertion order (sort.SliceStable).
func EventOrder(a, b SampleEvent) bool {
	if a.DTSNanos != b.DTSNanos {
		return a.DTSNanos < b.DTSNanos
	}
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	return a.SampleIndex < b.SampleIndex
}

// TrackTable holds the decoded sample-table pointers for one trak box.
type TrackTable struct {
	TrackID   uint32
	Timescale uint32
	Handler   string // hdlr handler_type, e.g. "soun"
	Entries   []SampleEvent
}

// tableReader abstracts the byte source the scanner hands to sample decoding.
type tableReader interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// sttsEntry / stscEntry are the locally decoded (version-agnostic) forms.
type sttsEntry struct {
	sampleCount uint32
	sampleDelta uint32
}

type stscEntry struct {
	firstChunk      uint32
	samplesPerChunk uint32
}

// chunkRun is one expanded stsc run: chunk range [first, last) with a fixed
// number of samples per chunk.
type chunkRun struct {
	firstChunk uint32
	lastChunk  uint32 // exclusive
	samples    uint32
}

// expandStsc turns the sparse stsc first-chunk table into explicit per-chunk
// runs covering every chunk [0, chunkCount).
func expandStsc(entries []stscEntry, chunkCount int) ([]chunkRun, error) {
	if len(entries) == 0 || entries[0].firstChunk != 1 {
		return nil, clock.NewError(clock.ErrTruncatedBox, "stsc must start at first chunk")
	}
	runs := make([]chunkRun, 0, len(entries))
	for i, e := range entries {
		last := uint32(chunkCount)
		if i+1 < len(entries) {
			last = entries[i+1].firstChunk - 1 // convert to inclusive chunk ordinal
		}
		runs = append(runs, chunkRun{
			firstChunk: e.firstChunk - 1, // stsc chunks are 1-based
			lastChunk:  last,
			samples:    e.samplesPerChunk,
		})
	}
	return runs, nil
}

// decodeStts expands time-to-sample entries into per-sample raw DTS ticks and
// durations. VFR is represented directly: no average frame rate is assumed.
func decodeStts(entries []sttsEntry, count int) ([]uint64, []uint64, error) {
	dts := make([]uint64, 0, count)
	dur := make([]uint64, 0, count)
	var t uint64
	for _, e := range entries {
		for i := uint32(0); i < e.sampleCount; i++ {
			dts = append(dts, t)
			dur = append(dur, uint64(e.sampleDelta))
			t += uint64(e.sampleDelta)
		}
	}
	if len(dts) != count {
		return nil, nil, clock.NewError(clock.ErrTruncatedBox,
			"stts entry count does not match stsz sample count")
	}
	return dts, dur, nil
}
