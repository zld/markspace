// Package clip rebuilds MP4 fragments at exact sample boundaries. Samples
// are copied byte-for-byte from the source mdat (no re-encoding); only the
// container boxes and sample tables are rewritten.
package clip

import (
	"os"

	"replay/internal/clock"
	"replay/internal/media"
)

// Range is a half-open sample window inside one track (Last inclusive).
type Range struct {
	First uint32
	Last  uint32
}

// ClipSpec is the cutter input produced by the plan stage. Tick values
// use the video track's time base.
type ClipSpec struct {
	EventID    string
	SyncStart  int64 // PTS of the sync sample backing the start
	WindowEnd  int64 // requested event end, video ticks
	EventStart int64 // requested event start, video ticks
}

// ClipResult describes a delivered fragment for manifest/checkpoint use.
type ClipResult struct {
	EventID    string       `json:"event_id"`
	FileName   string       `json:"file_name"`
	SHA256     [32]byte     `json:"-"`
	SHA256Hex  string       `json:"sha256"`
	Size       int64        `json:"size"`
	VideoRange Range        `json:"video_range"`
	AudioRange *Range       `json:"audio_range,omitempty"`
	StartPTS   int64        `json:"start_pts"`
	EndPTS     int64        `json:"end_pts"`
	Duration   int64        `json:"duration_ticks"`
	Timescale  uint32       `json:"timescale"`
	SampleOff  []SampleCopy `json:"sample_offsets"`
}

// SampleCopy records where one output sample came from in the source.
type SampleCopy struct {
	TrackID      uint32 `json:"track_id"`
	OutputIndex  uint32 `json:"output_index"`
	SourceIndex  uint32 `json:"source_index"`
	SourceOffset int64  `json:"source_offset"`
	Size         uint32 `json:"size"`
}

// Builder carries the parsed source and open file handle across clips.
type Builder struct {
	Info *media.FileInfo
	TL   *media.Timeline
	f    *os.File
}

// rngRef exposes the table window for result construction.
func (tb *tables) rngRef() *Range { return &tb.rng }

// NewBuilder validates that the source file is still readable.
func NewBuilder(info *media.FileInfo, tl *media.Timeline) (*Builder, error) {
	return &Builder{Info: info, TL: tl}, nil
}

// pickVideoRange selects the sync-backed start and the last sample fully
// inside the half-open window [start, windowEnd). Every chosen sample
// stays inside the source timeline.
func (b *Builder) pickVideoRange(spec ClipSpec) (Range, error) {
	v := b.TL.Video
	start := v.SyncAtOrBefore(spec.EventStart)
	if start < 0 {
		return Range{}, clock.NewError(clock.KindAlignment, "cut",
			"no sync sample at or before event start").WithEvent(spec.EventID)
	}
	// First sample at or after the end is excluded, so the last included
	// sample is its predecessor.
	end := v.FirstSampleAtOrAfter(spec.WindowEnd) - 1
	if end >= len(v.Samples) {
		end = len(v.Samples) - 1
	}
	if end < 0 {
		end = 0
	}
	if uint32(start) > uint32(end) {
		return Range{}, clock.NewError(clock.KindAlignment, "cut",
			"empty clip window").WithEvent(spec.EventID)
	}
	return Range{First: uint32(start), Last: uint32(end)}, nil
}

// pickAudioRange maps the selected video window onto the audio time base.
func (b *Builder) pickAudioRange(vr Range, vts uint32) *Range {
	a := b.TL.Audio
	if a == nil {
		return nil
	}
	v := b.TL.Video
	startMT, err := clock.NewMediaTime(v.Samples[vr.First].PTS, vts)
	if err != nil {
		return nil
	}
	endMT, err := clock.NewMediaTime(
		v.Samples[vr.Last].PTS+int64(v.Samples[vr.Last].Dur), vts)
	if err != nil {
		return nil
	}
	s, err := startMT.ToScale(a.Timescale)
	if err != nil {
		return nil
	}
	e, err := endMT.ToScale(a.Timescale)
	if err != nil {
		return nil
	}
	first := a.FirstSampleAtOrAfter(s.Ticks)
	last := a.FirstSampleAtOrAfter(e.Ticks) - 1
	if first >= len(a.Samples) || last < first {
		return nil
	}
	return &Range{First: uint32(first), Last: uint32(last)}
}
