// Package manifest parses HLS playlists and DASH MPDs into one exact integer
// timeline. Declared durations that do not land on the output timescale are
// kept as rational remainders and summed, never rounded away.
package manifest

import (
	"fmt"
	"math"

	"mediamux/media"
)

// Kind identifies the manifest dialect a document came from.
type Kind string

const (
	KindHLS  Kind = "hls"
	KindDASH Kind = "dash"
)

// OutputTimescale is the single timeline scale every Segment uses.
const OutputTimescale = media.Timescale90K

// Segment is one media segment on the unified timeline.
type Segment struct {
	Index           int             `json:"index"`
	ID              string          `json:"id"`
	Init            string          `json:"init,omitempty"`
	Start           media.Time      `json:"start"`
	Duration        media.Time      `json:"duration"`
	Declared        media.Ratio     `json:"declared"`
	Discontinuity   bool            `json:"discontinuity,omitempty"`
	SourceTicks     int64           `json:"source_ticks"`
	SourceTimescale media.Timescale `json:"source_timescale"`
	// DeclaredOffset is the byte offset of the declaration that carried this
	// duration, so a duration mismatch can be traced to the manifest line.
	DeclaredOffset int64 `json:"declared_offset"`
}

// End returns the segment end on the unified timeline.
func (s Segment) End() media.Time {
	return media.NewTime(s.Start.Value+s.Duration.Value, s.Start.Timescale)
}

// Document is the parsed, unified manifest.
type Document struct {
	Kind Kind `json:"kind"`
	// Version is the playlist/MPD format version declared by the document
	// (HLS EXT-X-VERSION), not this library's schema version.
	Version         int                    `json:"version"`
	TargetDuration  media.Ratio            `json:"target_duration"`
	MediaSequence   int64                  `json:"media_sequence"`
	Timescale       media.Timescale        `json:"timescale"`
	Segments        []Segment              `json:"segments"`
	Alignment       media.AlignAccumulator `json:"alignment"`
	TargetBPS       int64                  `json:"target_bps"`
	Discontinuities int                    `json:"discontinuities"`
}

// TotalDeclared is the exact sum of declared segment durations.
func (d *Document) TotalDeclared() media.Ratio {
	var total media.Ratio
	for _, s := range d.Segments {
		total = total.Add(s.Declared)
	}
	return total
}

// TotalQuantised is the span covered by the integer timeline.
func (d *Document) TotalQuantised() media.Ratio {
	if len(d.Segments) == 0 {
		return media.Ratio{}
	}
	first := d.Segments[0].Start.Value
	last := d.Segments[len(d.Segments)-1].End().Value
	return media.NewRatio(last-first, int64(OutputTimescale))
}

// builder accumulates an exact cursor and emits quantised segments.
type builder struct {
	kind      Kind
	seq       int64
	cum       media.Ratio
	segments  []Segment
	alignment media.AlignAccumulator
	disc      int
}

func newBuilder(kind Kind, seq int64) *builder {
	return &builder{kind: kind, seq: seq}
}

// add places a segment at the current exact cursor. start is the floor of the
// cursor on the output timescale; the sub-tick residual is recorded as
// alignment evidence rather than silently dropped.
func (b *builder) add(id, init string, declared media.Ratio, disc bool, srcTicks int64, srcTS media.Timescale, declOffset int64) error {
	if declared.Num < 0 {
		return media.NewError(media.CodeManifestDrift, media.PhaseManifest,
			fmt.Sprintf("negative duration for %q", id)).OnSegment(len(b.segments), id)
	}
	startTicks, startRem, err := floorTicks(b.cum, OutputTimescale)
	if err != nil {
		return err
	}
	b.cum = b.cum.Add(declared)
	endTicks, _, err := floorTicks(b.cum, OutputTimescale)
	if err != nil {
		return err
	}
	if endTicks < startTicks {
		return media.NewError(media.CodeManifestDrift, media.PhaseManifest,
			"timeline moved backwards").OnSegment(len(b.segments), id)
	}
	b.alignment.Observe(startRem)
	if disc {
		b.disc++
	}
	b.segments = append(b.segments, Segment{
		Index:           len(b.segments),
		ID:              id,
		Init:            init,
		Start:           media.NewTime(startTicks, OutputTimescale),
		Duration:        media.NewTime(endTicks-startTicks, OutputTimescale),
		Declared:        declared,
		Discontinuity:   disc,
		SourceTicks:     srcTicks,
		SourceTimescale: srcTS,
		DeclaredOffset:  declOffset,
	})
	b.seq++
	return nil
}

func (b *builder) finish(target media.Ratio, bps int64) *Document {
	return &Document{
		Kind:            b.kind,
		TargetDuration:  target,
		MediaSequence:   b.seq - int64(len(b.segments)),
		Timescale:       OutputTimescale,
		Segments:        b.segments,
		Alignment:       b.alignment,
		TargetBPS:       bps,
		Discontinuities: b.disc,
	}
}

// floorTicks converts r seconds to whole ticks at ts. The returned residual is
// the exact portion of r not represented by those ticks, in seconds.
func floorTicks(r media.Ratio, ts media.Timescale) (int64, media.Ratio, error) {
	if r.Den == 0 || r.Num == 0 {
		return 0, media.Ratio{}, nil
	}
	if r.Num < 0 {
		return 0, media.Ratio{}, media.NewError(media.CodeManifestDrift, media.PhaseManifest, "negative timeline position")
	}
	whole := r.Num / r.Den
	frac := r.Num % r.Den
	limit := int64(math.MaxInt64) / int64(ts)
	if whole > limit || frac > limit {
		return 0, media.Ratio{}, media.NewError(media.CodeManifestDrift, media.PhaseManifest, "timeline overflow")
	}
	ticks := whole*int64(ts) + frac*int64(ts)/r.Den
	// Exact residual: r - ticks/ts, in seconds.
	residual := r.Sub(media.NewRatio(ticks, int64(ts)))
	return ticks, residual, nil
}

// ErrLine builds a manifest error anchored to a byte offset.
func ErrLine(code media.Code, off int64, detail string) *media.Error {
	return media.NewError(code, media.PhaseManifest, detail).AtOffset(off)
}
