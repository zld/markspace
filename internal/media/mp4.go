package media

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/abema/go-mp4"
)

// ParseMP4 reads a single-track MP4 file and converts its sample table to
// the unified nanosecond clock. It rejects zero timescales and truncated
// sample payloads with a structured error carrying the offending offset.
func ParseMP4(path, trackKey string) (*Track, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, NewError(StageScan, "mp4.open", err.Error())
	}
	defer f.Close()

	info := &mp4Info{}
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdat():
			info.mdat = append(info.mdat, h.BoxInfo)
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			md, ok := box.(*mp4.Mdhd)
			if !ok {
				return nil, NewError(StageScan, "mp4.mdhd", "unexpected payload type")
			}
			info.timescale = int64(md.Timescale)
			info.duration = int64(md.GetDuration())
		case mp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.stts = box.(*mp4.Stts).Entries
		case mp4.BoxTypeStsc():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.stsc = box.(*mp4.Stsc).Entries
		case mp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			info.stsz = box.(*mp4.Stsz)
		case mp4.BoxTypeStco():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, o := range box.(*mp4.Stco).ChunkOffset {
				info.chunkOffsets = append(info.chunkOffsets, int64(o))
			}
		case mp4.BoxTypeCo64():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, o := range box.(*mp4.Co64).ChunkOffset {
				info.chunkOffsets = append(info.chunkOffsets, int64(o))
			}
		}
		return h.Expand()
	}

	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		return nil, wrapMP4ReadError(err)
	}
	return info.build(f, trackKey)
}

// mp4Info holds the extracted boxes while walking the structure.
type mp4Info struct {
	timescale    int64
	duration     int64
	stts         []mp4.SttsEntry
	stsc         []mp4.StscEntry
	stsz         *mp4.Stsz
	chunkOffsets []int64
	mdat         []mp4.BoxInfo
}

// build converts the raw boxes into validated Samples on one Track.
func (m *mp4Info) build(r io.ReadSeeker, trackKey string) (*Track, error) {
	if m.timescale == 0 {
		return nil, NewError(StageScan, "mp4.mdhd", "zero or missing timescale").WithTrack(trackKey)
	}
	tb, err := NewTimeBase(m.timescale)
	if err != nil {
		return nil, NewError(StageScan, "mp4.mdhd", err.Error()).WithTrack(trackKey)
	}
	if len(m.mdat) == 0 {
		return nil, NewError(StageScan, "mp4.mdat", "no media data box").WithTrack(trackKey)
	}
	sizes, err := m.sampleSizes()
	if err != nil {
		return nil, NewError(StageScan, "mp4.stsz", err.Error()).WithTrack(trackKey)
	}
	deltas, err := m.sampleDeltas(len(sizes))
	if err != nil {
		return nil, NewError(StageScan, "mp4.stts", err.Error()).WithTrack(trackKey)
	}
	offsets, err := m.sampleOffsets(len(sizes))
	if err != nil {
		return nil, NewError(StageScan, "mp4.stsc", err.Error()).WithTrack(trackKey)
	}

	track := &Track{Key: trackKey, TimeBase: tb}
	var tick int64
	for i := range sizes {
		startNs, err := tb.TicksToNs(tick)
		if err != nil {
			return nil, NewError(StageScan, "mp4.time", err.Error()).
				WithTrack(trackKey).WithOffset(offsets[i])
		}
		durNs, err := tb.DeltaNs(int64(deltas[i]))
		if err != nil {
			return nil, NewError(StageScan, "mp4.time", err.Error()).
				WithTrack(trackKey).WithOffset(offsets[i])
		}
		if err := m.checkPayload(r, offsets[i], int64(sizes[i])); err != nil {
			return nil, NewError(StageScan, "mp4.mdat", err.Error()).
				WithTrack(trackKey).WithOffset(offsets[i])
		}
		if err := track.Add(startNs, durNs, offsets[i], int64(sizes[i]), tick, nil); err != nil {
			return nil, err
		}
		tick += int64(deltas[i])
	}
	return track, nil
}

// sampleSizes expands the stsz box, honoring the uniform-size short form.
func (m *mp4Info) sampleSizes() ([]uint32, error) {
	if m.stsz == nil {
		return nil, errors.New("missing sample size box")
	}
	if m.stsz.SampleSize != 0 {
		out := make([]uint32, m.stsz.SampleCount)
		for i := range out {
			out[i] = m.stsz.SampleSize
		}
		return out, nil
	}
	if uint32(len(m.stsz.EntrySize)) != m.stsz.SampleCount {
		return nil, fmt.Errorf("stsz entry count %d mismatches declared %d",
			len(m.stsz.EntrySize), m.stsz.SampleCount)
	}
	return append([]uint32(nil), m.stsz.EntrySize...), nil
}

// sampleDeltas expands stts run-length entries into one delta per sample.
func (m *mp4Info) sampleDeltas(count int) ([]uint32, error) {
	out := make([]uint32, 0, count)
	for _, e := range m.stts {
		for j := uint32(0); j < e.SampleCount; j++ {
			out = append(out, e.SampleDelta)
		}
	}
	if len(out) != count {
		return nil, fmt.Errorf("stts produced %d deltas, expected %d", len(out), count)
	}
	return out, nil
}

// samplesPerChunkFor resolves the stsc run-length table for a chunk index.
func samplesPerChunkFor(entries []mp4.StscEntry, chunk uint32) (uint32, error) {
	if len(entries) == 0 {
		return 0, errors.New("empty sample-to-chunk table")
	}
	found := entries[0]
	for _, e := range entries {
		if e.FirstChunk <= chunk+1 { // stsc first_chunk is 1-based
			found = e
		} else {
			break
		}
	}
	if found.SamplesPerChunk == 0 {
		return 0, errors.New("zero samples per chunk")
	}
	return found.SamplesPerChunk, nil
}

// sampleOffsets walks chunks and expands them into per-sample byte offsets.
func (m *mp4Info) sampleOffsets(count int) ([]int64, error) {
	if len(m.chunkOffsets) == 0 {
		return nil, errors.New("missing chunk offset table")
	}
	out := make([]int64, 0, count)
	sizes, _ := m.sampleSizes()
	for ci, chunkStart := range m.chunkOffsets {
		per, err := samplesPerChunkFor(m.stsc, uint32(ci))
		if err != nil {
			return nil, err
		}
		cursor := chunkStart
		for s := uint32(0); s < per; s++ {
			idx := len(out)
			if idx >= count {
				return nil, fmt.Errorf("chunk %d yields more samples than stsz declares", ci)
			}
			out = append(out, cursor)
			cursor += int64(sizes[idx])
		}
	}
	if len(out) != count {
		return nil, fmt.Errorf("chunk walk produced %d offsets, expected %d", len(out), count)
	}
	return out, nil
}

// checkPayload verifies that [off, off+size) lies inside an mdat payload.
// Reaching past the file means the sample table points at truncated data.
func (m *mp4Info) checkPayload(r io.ReadSeeker, off, size int64) error {
	end := off + size
	for _, bi := range m.mdat {
		payloadStart := int64(bi.Offset + bi.HeaderSize)
		payloadEnd := int64(bi.Offset + bi.Size)
		if off >= payloadStart && end <= payloadEnd {
			return nil
		}
	}
	fileEnd, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if end > fileEnd {
		return fmt.Errorf("truncated sample payload: need %d bytes, file is %d", end, fileEnd)
	}
	return errors.New("sample offset outside any mdat payload")
}

// wrapMP4ReadError translates go-mp4/IO failures into the structured model,
// preserving the byte offset where the box walk gave up.
func wrapMP4ReadError(err error) error {
	var me *Error
	if errors.As(err, &me) {
		return err
	}
	msg := err.Error()
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		strings.Contains(msg, "box size exceeds file size") ||
		strings.Contains(msg, "too large box size") {
		base := NewError(StageScan, "mp4.read", "truncated box or unexpected end of file: "+msg)
		if off, ok := parseTruncationOffset(msg); ok {
			base = base.WithOffset(off)
		}
		return base
	}
	return NewError(StageScan, "mp4.read", msg)
}

// parseTruncationOffset extracts the "offset=%d" field emitted by go-mp4's
// payload-size check so truncation errors remain byte-addressable.
func parseTruncationOffset(msg string) (int64, bool) {
	idx := strings.Index(msg, "offset=")
	if idx < 0 {
		return 0, false
	}
	var v int64
	for i := idx + len("offset="); i < len(msg); i++ {
		c := msg[i]
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int64(c-'0')
	}
	return v, true
}
