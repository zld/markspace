// Package mp4 parses fragmented MP4 (CMAF-style) fragments with go-mp4 and
// projects tfdt/trun timing onto the shared rational clock. Only the local
// fragment subset is supported; truncated or structurally invalid fragments
// are reported as unrecoverable model errors.
package mp4

import (
	"bytes"
	"fmt"
	"io"

	go_mp4 "github.com/abema/go-mp4"

	"mediaqc/internal/adapter"
	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

// Sample is one trun sample entry with timing and payload size.
type Sample struct {
	Duration uint32
	Size     uint32
	Flags    uint32
	CTO      int32
}

// Fragment is the parsed subset of one fMP4 fragment.
type Fragment struct {
	TrackID        uint32
	BaseMediaTime  uint64
	Timescale      int64
	Samples        []Sample
	PayloadBytes   int64
	Sequence       uint64
}

// Parser implements adapter.Parser for the fMP4 subset.
type Parser struct {
	limit adapter.Limits
}

// NewParser builds a bounded parser.
func NewParser(limit adapter.Limits) *Parser { return &Parser{limit: limit} }

// Descriptor implements adapter.Parser.
func (p *Parser) Descriptor() adapter.Capability {
	return adapter.Capability{
		Kind: Kind(), Name: "fmp4-local", Version: "1.4.1",
		Features: []string{"frag-mp4", "tfdt", "trun"},
		Limit:    p.limit,
	}
}

// Supports implements adapter.Parser.
func (p *Parser) Supports(f string) bool { return p.Descriptor().Supports(f) }

// Kind returns the adapter kind.
func Kind() adapter.Kind { return adapter.KindMP4 }

func init() { adapter.MustRegister(NewParser(adapter.DefaultLimits())) }

// Parse reads one fragment. timescale is the track timescale declared in the
// init segment (mdhd); a zero timescale is rejected.
func (p *Parser) Parse(materialID, trackID string, timescale int64, seq uint64,
	r io.ReadSeeker) (*Fragment, error) {
	if timescale <= 0 {
		return nil, model.NewError(model.CodeZeroTimescale, model.StageParse, materialID, trackID,
			fmt.Sprintf("invalid track timescale %d", timescale))
	}
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, model.Wrap(err, model.CodeTruncatedFragment, model.StageParse, materialID, trackID, "seek failed")
	}
	if size > p.limit.MaxBytes {
		return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, trackID,
			fmt.Sprintf("fragment %d bytes exceeds limit", size))
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, model.Wrap(err, model.CodeTruncatedFragment, model.StageParse, materialID, trackID, "seek failed")
	}
	frag := &Fragment{Timescale: timescale, Sequence: seq}
	// BoxPaths are outer-first, matching the parsed box hierarchy.
	paths := []go_mp4.BoxPath{
		{go_mp4.BoxTypeMoof(), go_mp4.BoxTypeTraf(), go_mp4.BoxTypeTfhd()},
		{go_mp4.BoxTypeMoof(), go_mp4.BoxTypeTraf(), go_mp4.BoxTypeTfdt()},
		{go_mp4.BoxTypeMoof(), go_mp4.BoxTypeTraf(), go_mp4.BoxTypeTrun()},
		{go_mp4.BoxTypeMdat()},
	}
	boxes, err := go_mp4.ExtractBoxesWithPayload(r, nil, paths)
	if err != nil {
		return nil, model.Wrap(err, model.CodeTruncatedFragment, model.StageParse, materialID, trackID,
			"fragment box extraction failed")
	}
	sawTfdt, sawTrun := false, false
	for _, b := range boxes {
		switch payload := b.Payload.(type) {
		case *go_mp4.Tfhd:
			frag.TrackID = payload.TrackID
		case *go_mp4.Tfdt:
			frag.BaseMediaTime = payload.GetBaseMediaDecodeTime()
			sawTfdt = true
		case *go_mp4.Trun:
			sawTrun = true
			if int(payload.SampleCount) > p.limit.MaxSegments {
				return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, trackID,
					"sample count exceeds limit")
			}
			for _, e := range payload.Entries {
				frag.Samples = append(frag.Samples, Sample{
					Duration: e.SampleDuration, Size: e.SampleSize,
					Flags: e.SampleFlags, CTO: ctoOf(e),
				})
			}
		case *go_mp4.Mdat:
			frag.PayloadBytes = int64(b.Info.Size) - int64(b.Info.HeaderSize)
		}
	}
	if !sawTfdt || !sawTrun {
		return nil, model.NewError(model.CodeTruncatedFragment, model.StageParse, materialID, trackID,
			"fragment missing tfdt or trun box")
	}
	return frag, nil
}

// ParseBytes is a convenience wrapper.
func (p *Parser) ParseBytes(materialID, trackID string, timescale int64, seq uint64, b []byte) (*Fragment, error) {
	return p.Parse(materialID, trackID, timescale, seq, bytes.NewReader(b))
}

// Event converts a fragment into the unified model using exact clock math.
func (f *Fragment) Event(materialID, trackID string, kind model.TrackKind) (model.Event, error) {
	tb, err := clock.New(f.Timescale)
	if err != nil {
		return model.Event{}, model.Wrap(err, model.CodeZeroTimescale, model.StageParse, materialID, trackID, err.Error())
	}
	var totalDur int64
	var totalSize int64
	for _, s := range f.Samples {
		totalDur += int64(s.Duration)
		totalSize += int64(s.Size)
	}
	return model.Event{
		MaterialID: materialID,
		TrackID:    trackID,
		Kind:       kind,
		Sequence:   f.Sequence,
		Timebase:   tb,
		Start:      tb.At(int64(f.BaseMediaTime)),
		Duration:   tb.At(totalDur),
		PTS:        tb.At(int64(f.BaseMediaTime)),
		Size:       int(totalSize),
		Origin:     fmt.Sprintf("frag-%d-track-%d", f.Sequence, f.TrackID),
	}, nil
}

func ctoOf(e go_mp4.TrunEntry) int32 { return e.SampleCompositionTimeOffsetV1 }
