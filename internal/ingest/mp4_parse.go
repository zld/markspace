package ingest

import (
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediavault/internal/timeline"
)

// ParseMP4 parses a project-generated MP4 fixture into per-track samples.
// Per-track failures are collected on ParsedTrack.Errors and never abort the
// other tracks; a structurally unreadable file returns a StageError.
func ParseMP4(path, source string) (*MP4Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var traks, mdats []*BoxHeader
	seenFtyp := false
	err = walkBoxes(f, 0, -1, containerBoxes, func(h *BoxHeader, _ int64) error {
		switch h.Type {
		case boxFtyp:
			seenFtyp = true
		case boxTrak:
			traks = append(traks, h)
		case boxMdat:
			mdats = append(mdats, h)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !seenFtyp {
		return nil, &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindMissingTrack,
			Offset: 0, Recoverable: false, Detail: "ftyp box missing; not an MP4 fixture",
		}
	}
	if len(traks) == 0 {
		return nil, &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindMissingTrack,
			Offset: 0, Recoverable: false, Detail: "moov contains no trak boxes",
		}
	}
	if len(mdats) == 0 {
		return nil, &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindMissingTrack,
			Offset: 0, Recoverable: false, Detail: "mdat box missing; no media payload",
		}
	}

	res := &MP4Result{Source: source}
	for i, trak := range traks {
		pt := &ParsedTrack{
			ID: timeline.TrackID{Source: source, Index: uint32(i), Kind: timeline.KindUnknown},
		}
		mt := &mp4Track{index: uint32(i), mdat: mdats[0]}
		if perr := parseTrackTables(f, trak, mt); perr != nil {
			pt.Errors = append(pt.Errors, asStage(perr))
			res.Tracks = append(res.Tracks, pt)
			continue
		}
		pt.ID.Kind = mt.kind
		pt.Timescale = mt.timescale
		samples, serr := expandSamples(f, mt, pt.ID)
		if serr != nil {
			pt.Errors = append(pt.Errors, serr)
		}
		pt.Samples = samples
		res.Tracks = append(res.Tracks, pt)
	}
	return res, nil
}

// expandSamples turns stts/stsz/stsc/stco tables into ordered Samples with
// exact rational timestamps and payload bytes sliced from mdat. Every byte
// range is bounds-checked so a truncated mdat reports its real offset.
func expandSamples(r io.ReadSeeker, t *mp4Track, id timeline.TrackID) ([]timeline.Sample, *timeline.StageError) {
	if t.timescale == 0 {
		return nil, &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindMissingTrack,
			Track: id, Recoverable: false, Detail: "track timescale is zero",
		}
	}
	dts := int64(0)
	norm := timeline.NewTrackNormalizer(id, timeline.ClockRef{Source: id.Source, Clock: id.Index}, t.timescale, 0)
	var out []timeline.Sample
	for _, e := range t.stts {
		for k := uint32(0); k < e.SampleCount; k++ {
			ts, err := norm.PushSample(dts, dts)
			if err != nil {
				return out, asStage(err)
			}
			out = append(out, timeline.Sample{Track: id, Order: ts.Order, TS: *ts})
			dts += int64(e.SampleDelta)
		}
	}
	// Assign payload byte ranges via chunk layout.
	if err := assignPayload(r, t, id, out); err != nil {
		return out, err
	}
	return out, nil
}

// assignPayload walks chunk offsets and per-sample sizes and fills Payload.
// stsc is run-length encoded: entry i applies from FirstChunk until the next
// entry's FirstChunk.
func assignPayload(r io.ReadSeeker, t *mp4Track, id timeline.TrackID, out []timeline.Sample) *timeline.StageError {
	mdatEnd := int64(-1)
	if t.mdat.Size > 0 {
		mdatEnd = t.mdat.Offset + t.mdat.Size
	}
	sampleIdx := uint32(0)
	for chunkIdx := 0; chunkIdx < len(t.offsets); chunkIdx++ {
		samplesInChunk := uint32(samplesPerChunkAt(t.stsc, uint32(chunkIdx)))
		off := int64(t.offsets[chunkIdx])
		for s := uint32(0); s < samplesInChunk; s++ {
			size := t.sampleSize(sampleIdx)
			if size == 0 {
				return &timeline.StageError{
					Stage: timeline.StageIngest, Kind: timeline.KindTruncatedCapture,
					Offset: off, Position: uint64(sampleIdx), Track: id, Recoverable: true,
					Detail: fmt.Sprintf("sample %d has zero size", sampleIdx),
				}
			}
			if mdatEnd > 0 && off+int64(size) > mdatEnd {
				return &timeline.StageError{
					Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
					Offset: off, Position: uint64(sampleIdx), Track: id, Recoverable: false,
					Detail: fmt.Sprintf("sample %d bytes [%d,%d) exceed mdat end %d", sampleIdx, off, off+int64(size), mdatEnd),
				}
			}
			buf := make([]byte, size)
			if _, err := r.Seek(off, io.SeekStart); err != nil {
				return asStage(err)
			}
			if _, err := io.ReadFull(r, buf); err != nil {
				return &timeline.StageError{
					Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
					Offset: off, Position: uint64(sampleIdx), Track: id, Recoverable: false,
					Detail: fmt.Sprintf("sample %d payload read truncated: %v", sampleIdx, err), Err: err,
				}
			}
			if int(sampleIdx) < len(out) {
				out[sampleIdx].Payload = buf
			}
			sampleIdx++
			off += int64(size)
		}
	}
	return nil
}

// samplesPerChunkAt resolves the stsc run covering chunk index ci.
// FirstChunk values are 1-based, so compare against ci+1.
func samplesPerChunkAt(stsc []mp4.StscEntry, ci uint32) uint32 {
	samples := uint32(0)
	oneBased := ci + 1
	for _, e := range stsc {
		if e.FirstChunk > oneBased {
			break
		}
		samples = e.SamplesPerChunk
	}
	return samples
}

func (t *mp4Track) sampleSize(i uint32) uint32 {
	if t.uniform != 0 {
		return t.uniform
	}
	if int(i) < len(t.sizes) {
		return t.sizes[i]
	}
	return 0
}

func asStage(err error) *timeline.StageError {
	if se, ok := err.(*timeline.StageError); ok {
		return se
	}
	return &timeline.StageError{
		Stage: timeline.StageIngest, Kind: timeline.KindTruncatedCapture,
		Recoverable: false, Detail: err.Error(), Err: err,
	}
}
