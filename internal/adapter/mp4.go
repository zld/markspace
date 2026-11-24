package adapter

import (
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"

	"mediad/internal/media"
)

// MP4Parser demultiplexes ISO-BMFF files with go-mp4, preserving compressed
// sample bytes verbatim and parsing edit lists into media.Edit entries.
type MP4Parser struct{}

func (MP4Parser) Name() string    { return "mp4" }
func (MP4Parser) Version() string { return ParserAPIVersion }

// Probe matches the ftyp box signature at the start of the file.
func (MP4Parser) Probe(head []byte) int {
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		return 100
	}
	return 0
}

// mp4TrackState accumulates one trak's boxes during the box walk.
type mp4TrackState struct {
	trackID   int
	timescale uint32
	language  string
	handler   string
	edits     []media.Edit
	stts      []mp4.SttsEntry
	stss      []uint32
	stsz      []uint32
	chunks    []uint64
	stsc      []mp4.StscEntry
	stsdRaw   []byte
	hasStss   bool
}

// Parse walks the box tree, then materializes sample events. A truncated
// mdat does not abort the parse: affected samples are marked damaged and
// recorded as a recoverable gap.
func (MP4Parser) Parse(r io.ReadSeeker) (*Stream, error) {
	var tracks []*mp4TrackState
	var cur *mp4TrackState
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeEdts(), mp4.BoxTypeDinf():
			return h.Expand()
		case mp4.BoxTypeTrak():
			cur = &mp4TrackState{trackID: -1}
			tracks = append(tracks, cur)
			return h.Expand()
		case mp4.BoxTypeTkhd():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			cur.trackID = int(box.(*mp4.Tkhd).TrackID)
		case mp4.BoxTypeMdhd():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			mdhd := box.(*mp4.Mdhd)
			cur.timescale = mdhd.Timescale
			cur.language = decodeLanguage(mdhd.Language)
		case mp4.BoxTypeHdlr():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			cur.handler = string(box.(*mp4.Hdlr).HandlerType[:])
		case mp4.BoxTypeElst():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			// edts is walked before mdia/mdhd, so the timescale is not
			// known yet; keep raw ticks and convert in materializeTrack.
			elst := box.(*mp4.Elst)
			for i := range elst.Entries {
				cur.edits = append(cur.edits, media.Edit{
					MediaTime: elst.GetMediaTime(i),
					Duration:  int64(elst.GetSegmentDuration(i)),
					Rate:      1,
				})
			}
		case mp4.BoxTypeStts():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			cur.stts = box.(*mp4.Stts).Entries
		case mp4.BoxTypeStss():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			cur.stss = box.(*mp4.Stss).SampleNumber
			cur.hasStss = true
		case mp4.BoxTypeStsz():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			stsz := box.(*mp4.Stsz)
			cur.stsz = stsz.EntrySize
			if stsz.SampleSize != 0 { // constant-size form
				cur.stsz = make([]uint32, stsz.SampleCount)
				for i := range cur.stsz {
					cur.stsz[i] = stsz.SampleSize
				}
			}
		case mp4.BoxTypeStco():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			for _, o := range box.(*mp4.Stco).ChunkOffset {
				cur.chunks = append(cur.chunks, uint64(o))
			}
		case mp4.BoxTypeCo64():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			cur.chunks = append(cur.chunks, box.(*mp4.Co64).ChunkOffset...)
		case mp4.BoxTypeStsc():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			cur.stsc = box.(*mp4.Stsc).Entries
		case mp4.BoxTypeStsd():
			// Kept as raw bytes: unknown fields stay available for audit.
			buf := make([]byte, 0, h.BoxInfo.Size-h.BoxInfo.HeaderSize)
			if _, e := h.ReadData(newWriterTo(&buf)); e != nil {
				return nil, e
			}
			cur.stsdRaw = buf
		}
		return nil, nil
	})
	if err != nil && err != io.EOF {
		return nil, media.New(media.PhaseDemux, -1, -1, "box walk", err)
	}

	st := &Stream{}
	for _, ts := range tracks {
		tr, gaps := materializeTrack(r, ts)
		st.Tracks = append(st.Tracks, tr)
		st.Gaps = append(st.Gaps, gaps...)
	}
	return st, nil
}

// convertEdits maps raw tick-domain edit entries to nanoseconds once the
// media timescale is known. Empty edits (media time -1) stay -1.
func convertEdits(raw []media.Edit, timescale uint32) []media.Edit {
	if len(raw) == 0 {
		return nil
	}
	out := make([]media.Edit, len(raw))
	for i, e := range raw {
		out[i] = e
		if e.MediaTime >= 0 {
			out[i].MediaTime = media.TicksToNanos(e.MediaTime, timescale)
		}
		out[i].Duration = media.TicksToNanos(e.Duration, timescale)
	}
	return out
}

// writerTo adapts a *[]byte to io.Writer for ReadData.
type writerTo struct{ p *[]byte }

func newWriterTo(p *[]byte) io.Writer { return &writerTo{p: p} }

func (w *writerTo) Write(b []byte) (int, error) {
	*w.p = append(*w.p, b...)
	return len(b), nil
}

// materializeTrack expands stts/stsc/stco/stsz into sample events and reads
// each compressed payload. Short reads (truncated mdat) mark the sample
// damaged and extend the track's gap record instead of failing the parse.
func materializeTrack(r io.ReadSeeker, ts *mp4TrackState) (*Track, []media.Gap) {
	tr := &Track{
		ID:               ts.trackID,
		Type:             handlerType(ts.handler),
		Codec:            stsdCodec(ts.stsdRaw),
		CodecParams:      ts.stsdRaw,
		Language:         ts.language,
		Timescale:        ts.timescale,
		Edits:            convertEdits(ts.edits, ts.timescale),
		HasKeyframeTable: ts.hasStss,
	}
	offsets := sampleOffsets(ts.stsc, ts.chunks, ts.stsz)
	key := map[int]bool{}
	for _, n := range ts.stss {
		key[int(n)-1] = true
	}
	var cts int64
	idx := 0
	var gaps []media.Gap
	gapStart := -1
	for _, e := range ts.stts {
		for k := uint32(0); k < e.SampleCount && idx < len(ts.stsz); k++ {
			ev := media.Event{
				TrackID:   tr.ID,
				Seq:       idx,
				MediaTime: media.TicksToNanos(cts, ts.timescale),
				Duration:  media.TicksToNanos(int64(e.SampleDelta), ts.timescale),
				Keyframe:  key[idx],
			}
			if idx < len(offsets) {
				ev.Payload, ev.Damaged = readSample(r, offsets[idx], ts.stsz[idx])
			} else {
				ev.Damaged = true
			}
			if ev.Damaged {
				if gapStart < 0 {
					gapStart = idx
				}
			} else if gapStart >= 0 {
				gaps = append(gaps, media.Gap{TrackID: tr.ID, FromSeq: gapStart, ToSeq: idx, Reason: "truncated sample data"})
				gapStart = -1
			}
			tr.Events = append(tr.Events, ev)
			cts += int64(e.SampleDelta)
			idx++
		}
	}
	if gapStart >= 0 {
		gaps = append(gaps, media.Gap{TrackID: tr.ID, FromSeq: gapStart, ToSeq: idx, Reason: "truncated sample data"})
	}
	if len(tr.Events) > 0 {
		tr.StartOffset = tr.Events[0].MediaTime
	}
	return tr, gaps
}

// readSample reads size bytes at offset; a short read reports damage.
func readSample(r io.ReadSeeker, off uint64, size uint32) ([]byte, bool) {
	buf := make([]byte, size)
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
		return nil, true
	}
	n, err := io.ReadFull(r, buf)
	if err != nil && n == 0 {
		return nil, true
	}
	return buf[:n], n < int(size)
}

// sampleOffsets expands the stsc/stco/stsz tables into per-sample offsets.
func sampleOffsets(stsc []mp4.StscEntry, chunks []uint64, stsz []uint32) []uint64 {
	out := make([]uint64, 0, len(stsz))
	si := 0
	for ci, coff := range chunks {
		chunk := uint32(ci) + 1
		var spc uint32
		for _, e := range stsc { // stsc is sorted; last match wins
			if e.FirstChunk <= chunk {
				spc = e.SamplesPerChunk
			}
		}
		off := coff
		for k := uint32(0); k < spc && si < len(stsz); k++ {
			out = append(out, off)
			off += uint64(stsz[si])
			si++
		}
	}
	return out
}

// stsdCodec extracts the first sample entry's fourcc from raw stsd bytes.
func stsdCodec(raw []byte) string {
	// stsd payload: 4B version/flags, 4B entry count, then entries as
	// [size 4B][fourcc 4B]...
	if len(raw) < 16 {
		return ""
	}
	if binary.BigEndian.Uint32(raw[4:8]) == 0 {
		return ""
	}
	return string(raw[12:16])
}

// decodeLanguage unpacks the 5-bit-per-char ISO-639-2 form stored in mdhd.
func decodeLanguage(l [3]byte) string {
	b := []byte{l[0] + 0x60, l[1] + 0x60, l[2] + 0x60}
	return string(b)
}

func handlerType(h string) media.TrackType {
	switch h {
	case "vide":
		return media.TrackVideo
	case "soun":
		return media.TrackAudio
	default:
		return media.TrackData
	}
}
