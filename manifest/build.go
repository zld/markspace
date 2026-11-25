package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/zld/media-regress/audio"
	"github.com/zld/media-regress/internal/clock"
	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/media"
)

// Builder assembles a manifest incrementally from parsed tracks and the
// converted audio stream, so a long publication never needs duplicate copies
// of every segment.
type Builder struct {
	m           *Manifest
	audioTarget AudioTarget
	conflicts   int
	dropSegment int // 1-based segment index to omit, 0 to keep all
}

// NewBuilder starts a manifest for source.
func NewBuilder(source string, format Format, inputDigest string) *Builder {
	return &Builder{m: Blank(source, format, inputDigest)}
}

// SetAudioTarget records the published audio parameters.
func (b *Builder) SetAudioTarget(t AudioTarget) { b.audioTarget = t }

// DropSegment asks the builder to omit the given 1-based segment, simulating a
// publication gap. Verification reports it; the builder never hides it.
func (b *Builder) DropSegment(index int) { b.dropSegment = index }

// AddTrack records a video representation or the audio track header.
func (b *Builder) AddTrack(t *media.Track) error {
	b.conflicts += t.Conflicts
	start, end, err := trackWindow(t)
	if err != nil {
		return err
	}
	if t.Kind != media.KindVideo {
		b.m.Track.ID = t.ID
		b.m.Track.Kind = string(t.Kind)
		b.m.Track.Timescale = t.Timescale
		b.m.Track.Audio = b.audioTarget
		b.m.Track.Conflicts = t.Conflicts
		b.m.Track.StartTick = start
		b.m.Track.EndTick = end
		return nil
	}
	rep := Representation{
		ID:        "video-" + strconv.FormatUint(uint64(t.ID), 10),
		Kind:      string(t.Kind),
		TrackID:   t.ID,
		StartTick: start,
		EndTick:   end,
		Timescale: t.Timescale,
		Duration:  t.Duration,
	}
	if t.Video != nil {
		rep.Render = Render{
			Width:    t.Video.Width,
			Height:   t.Video.Height,
			Rotation: t.Video.Rotation,
			Crop:     cropOf(t.Video.Crop),
			Colour:   colourOf(t.Video.Colour),
		}
	}
	b.m.Representations = append(b.m.Representations, rep)
	return nil
}

func cropOf(c *media.Crop) *Crop {
	if c == nil {
		return nil
	}
	return &Crop{Width: c.Width, Height: c.Height, HorizOffset: c.HorizOffset, VertOffset: c.VertOffset}
}

func colourOf(c *media.Colour) *Colour {
	if c == nil {
		return nil
	}
	return &Colour{Primaries: c.Primaries, Transfer: c.Transfer, Matrix: c.Matrix, FullRange: c.FullRange}
}

// trackWindow converts a track's duration into master ticks.
func trackWindow(t *media.Track) (int64, int64, error) {
	end, err := t.EndMaster()
	if err != nil {
		return 0, 0, err
	}
	if end <= 0 {
		return 0, 0, merr.New(merr.StageManifest, merr.KindContinuity, t.ID, t.InputOffset,
			"track %d has non-positive duration in master ticks", t.ID)
	}
	return 0, end, nil
}

// AddSegment appends one published audio segment. The segment's ticks are in
// the master timebase; continuity is checked by Verify, not silently closed.
func (b *Builder) AddSegment(seg Segment) {
	if b.dropSegment != 0 && seg.Index == b.dropSegment {
		return
	}
	b.m.Track.Segments = append(b.m.Track.Segments, seg)
}

// SegmentFromBlock converts a converted block and its start tick into a
// segment. The end tick is derived from the block's output frame count at the
// target sample rate, so segment windows tile exactly.
func SegmentFromBlock(trackID uint32, block audio.Block, startTick int64, targetRate uint32, digest string) (Segment, error) {
	if block.Frames <= 0 {
		return Segment{}, merr.New(merr.StageManifest, merr.KindInconsistent, trackID, int64(block.Index),
			"block %d produced no frames", block.Index)
	}
	dur, err := clock.ConvertTicks(int64(block.Frames), clock.Rate(targetRate), clock.Master)
	if err != nil {
		return Segment{}, err
	}
	return Segment{
		Index:     block.Index,
		TrackID:   trackID,
		StartTick: startTick,
		EndTick:   startTick + dur,
		Frames:    block.Frames,
		Digest:    digest,
		Bytes:     len(block.PCM),
	}, nil
}

// SetAudioWindow rewrites the audio track window from actual segments once they
// are all added. This keeps the published window aligned with what was emitted.
func (b *Builder) SetAudioWindow() {
	segs := b.m.Track.Segments
	if len(segs) == 0 {
		return
	}
	first, last := segs[0], segs[len(segs)-1]
	for _, s := range segs {
		if s.StartTick < first.StartTick {
			first = s
		}
		if s.EndTick > last.EndTick {
			last = s
		}
	}
	b.m.Track.StartTick = first.StartTick
	b.m.Track.EndTick = last.EndTick
}

// Finish records the run summary and computes the manifest digest.
func (b *Builder) Finish(sum Summary) error {
	sum.Conflicts = b.conflicts
	sum.ManifestSegments = len(b.m.Track.Segments)
	b.m.Summary = sum
	if len(b.m.Representations) == 0 {
		return merr.New(merr.StageManifest, merr.KindContinuity, 0, 0,
			"manifest has no video representations")
	}
	if len(b.m.Track.Segments) == 0 {
		return merr.New(merr.StageManifest, merr.KindContinuity, b.m.Track.ID, 0,
			"manifest has no audio segments")
	}
	return b.m.Finalize()
}

// Manifest returns the built manifest.
func (b *Builder) Manifest() *Manifest { return b.m }

// SegmentDigest returns the digest used for a converted block payload.
func SegmentDigest(pcm []byte) string {
	sum := sha256.Sum256(pcm)
	return hex.EncodeToString(sum[:])
}
