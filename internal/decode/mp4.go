// Package decode parses self-generated MP4 fragments and RTP/ID3 captures.
// Parsed events are streamed through callbacks one sample/packet at a time;
// full media payloads are never held in memory. Every event carries its raw
// byte offset so downstream stages can always locate the source bytes.
package decode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"medfix/internal/timeline"
)

// SampleEvent is emitted per parsed sample/box. Track metadata arrives once
// through OnTrack before that track's events.
type SampleEvent struct {
	Track       timeline.Track
	Event       timeline.Event
	IsTrackMeta bool
}

// MP4Sink receives streamed parser output.
type MP4Sink interface {
	OnTrack(t timeline.Track)
	OnEvent(e timeline.Event)
}

// stblContext accumulates one track's sample tables during the walk.
type stblContext struct {
	trackID string
	scale   uint32
	stts    []mp4.SttsEntry
	stss    []uint32
	stsz    []uint32
	stsc    []mp4.StscEntry
	stco    []uint64
	co64    []uint64
}

// ParseMP4 walks an MP4 stream and emits tracks, samples and emsg boxes.
// Truncated boxes or sample tables produce timeline.Error with the raw
// offset of the failing box instead of being silently skipped.
func ParseMP4(r io.ReadSeeker, sourceID string, sink MP4Sink) error {
	parser := &mp4Parser{
		r: r, sourceID: sourceID, sink: sink,
		tracks: map[uint32]*stblContext{},
	}
	_, err := mp4.ReadBoxStructure(r, parser.handle)
	if err != nil {
		var te *timeline.Error
		if errors.As(err, &te) {
			return err
		}
		return timeline.NewError(timeline.CodeTruncatedBox, "parse", "", -1,
			"mp4 structure read failed", err)
	}
	return parser.finish()
}

type mp4Parser struct {
	r            io.ReadSeeker
	sourceID     string
	sink         MP4Sink
	tracks       map[uint32]*stblContext
	curTrack     uint32
	seq          int
	emsgTrack    bool
	emsgScale    uint32
}

func (p *mp4Parser) handle(h *mp4.ReadHandle) (interface{}, error) {
	switch h.BoxInfo.Type.String() {
	case "moov", "trak", "mdia", "minf", "stbl", "moof", "traf":
		return h.Expand()

	case "mvhd":
		return p.readMvhd(h)

	case "tkhd":
		return p.readTkhd(h)

	case "mdhd":
		return p.readMdhd(h)

	case "stts", "stss", "stsz", "stsc", "stco", "co64":
		return p.readSampleTable(h)

	case "emsg":
		return p.readEmsg(h)

	case "mdat":
		// Payload bytes are read per sample later; never bulk-load mdat.
		return nil, nil
	}
	return nil, nil
}

func (p *mp4Parser) readMvhd(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, p.truncErr(h, "mvhd payload", err)
	}
	// Timescale lives on mdhd per track; mvhd only validates readability.
	_ = box.(*mp4.Mvhd)
	return nil, nil
}

func (p *mp4Parser) readTkhd(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, p.truncErr(h, "tkhd payload", err)
	}
	tkhd := box.(*mp4.Tkhd)
	p.curTrack = tkhd.TrackID
	if p.tracks[tkhd.TrackID] == nil {
		p.tracks[tkhd.TrackID] = &stblContext{trackID: fmt.Sprintf("%s/t%d", p.sourceID, tkhd.TrackID)}
	}
	return nil, nil
}

func (p *mp4Parser) readMdhd(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, p.truncErr(h, "mdhd payload", err)
	}
	mdhd := box.(*mp4.Mdhd)
	ctx := p.tracks[p.curTrack]
	if ctx == nil {
		ctx = &stblContext{trackID: fmt.Sprintf("%s/t%d", p.sourceID, p.curTrack)}
		p.tracks[p.curTrack] = ctx
	}
	ctx.scale = mdhd.Timescale
	return nil, nil
}

func (p *mp4Parser) readSampleTable(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, p.truncErr(h, h.BoxInfo.Type.String()+" payload", err)
	}
	ctx := p.tracks[p.curTrack]
	if ctx == nil {
		ctx = &stblContext{trackID: fmt.Sprintf("%s/t%d", p.sourceID, p.curTrack)}
		p.tracks[p.curTrack] = ctx
	}
	switch b := box.(type) {
	case *mp4.Stts:
		ctx.stts = b.Entries
	case *mp4.Stss:
		ctx.stss = b.SampleNumber
	case *mp4.Stsz:
		if b.SampleSize != 0 {
			ctx.stsz = make([]uint32, b.SampleCount)
			for i := range ctx.stsz {
				ctx.stsz[i] = b.SampleSize
			}
		} else {
			ctx.stsz = b.EntrySize
		}
	case *mp4.Stsc:
		ctx.stsc = b.Entries
	case *mp4.Stco:
		ctx.stco = make([]uint64, len(b.ChunkOffset))
		for i, off := range b.ChunkOffset {
			ctx.stco[i] = uint64(off)
		}
	case *mp4.Co64:
		ctx.co64 = b.ChunkOffset
	}
	return nil, nil
}

func (p *mp4Parser) readEmsg(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, p.truncErr(h, "emsg payload", err)
	}
	emsg := box.(*mp4.Emsg)
	if !p.emsgTrack {
		p.emsgTrack = true
		p.emsgScale = emsg.Timescale
		p.sink.OnTrack(timeline.Track{
			ID: p.sourceID + "/emsg", SourceID: p.sourceID,
			Timescale: emsg.Timescale,
		})
	}
	raw := bytes.Clone(emsg.MessageData)
	off := int64(h.BoxInfo.Offset)
	p.seq++
	ev := timeline.Event{
		Kind: timeline.KindEmsg, TrackID: p.sourceID + "/emsg",
		SourceID: p.sourceID, Seq: p.seq,
		RawTS:      int64(emsg.PresentationTimeDelta),
		RawPayload: raw, ContentHash: hashBytes(raw),
		Offset: timeline.OffsetRange{Begin: off, End: off + int64(h.BoxInfo.Size)},
	}
	// Version 1 uses an absolute 64-bit presentation time instead.
	if emsg.GetVersion() == 1 {
		ev.RawTS = int64(emsg.PresentationTime)
	}
	p.sink.OnEvent(ev)
	return nil, nil
}

func (p *mp4Parser) truncErr(h *mp4.ReadHandle, what string, inner error) error {
	return timeline.NewError(timeline.CodeTruncatedBox, "parse",
		fmt.Sprintf("%s/t%d", p.sourceID, p.curTrack),
		int64(h.BoxInfo.Offset), "truncated "+what, inner)
}

// finish materializes samples from the accumulated tables, reading each
// sample's bytes individually from mdat (streaming, no full mdat load).
func (p *mp4Parser) finish() error {
	for id, ctx := range p.tracks {
		if ctx.scale == 0 {
			return timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID,
				-1, "missing mdhd timescale", nil)
		}
		track := timeline.Track{ID: ctx.trackID, SourceID: p.sourceID, Timescale: ctx.scale, IsKeyframe: len(ctx.stss) > 0}
		p.sink.OnTrack(track)
		if err := p.emitSamples(id, ctx, track); err != nil {
			return err
		}
	}
	return nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
