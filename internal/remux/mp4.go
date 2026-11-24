// Package remux writes a lossless MP4 whose media payload bytes are copied
// verbatim from the demuxed input.  Only container structure (headers, sample
// tables, event boxes) is rebuilt, so input and output payload SHA-256 match
// for every valid input.
package remux

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/demux"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

// Plan is the fully resolved remux input: mapped tracks, ordered events and
// the source bytes the payload is copied from.
type Plan struct {
	Src        []byte
	Tracks     []demux.Track
	Mapping    track.Map
	Events     metadata.Merged
	MajorBrand string
	Cut        []demux.Range
	// Timescale is the movie timescale.  When zero it is the least common
	// multiple of every track and event timescale, so no time is rounded.
	Timescale uint32
}

// movieTimescale returns the exact common timescale for the plan, or the
// supplied override.
func (p Plan) movieTimescale() uint32 {
	if p.Timescale != 0 {
		return p.Timescale
	}
	scales := []uint32{p.Events.Timescale}
	for _, tr := range p.Tracks {
		scales = append(scales, tr.Timescale)
	}
	ts, err := clock.Unify(scales...)
	if err != nil || ts == 0 {
		return 1000
	}
	return ts
}

// Result describes what was written.
type Result struct {
	Bytes         int64
	PayloadSHA256 string
	TrackCount    int
	EventCount    int
	PayloadBytes  int64
}

// media is the ordered payload and per-track chunk offsets.
type media struct {
	payload []byte
	chunks  map[uint32][]uint32
	sizes   map[uint32][]uint32
	order   []uint32
	// trackByID keeps the sample list in step with the sizes so the sync flag
	// and timing tables can be rebuilt without re-deriving them.
	trackByID map[uint32]demux.Track
}

// relocate rewrites chunk offsets now that the mdat payload position is known.
func (m *media) relocate(first int64) {
	pos := first
	for _, id := range m.order {
		for i := range m.chunks[id] {
			m.chunks[id][i] = uint32(pos)
			pos += int64(m.sizes[id][i])
		}
	}
}

// Write emits the MP4 to w.  It fails before writing a single byte if the
// mapping is incomplete or duplicated, so a partial file can never be mistaken
// for a successful remux.
func Write(w io.Writer, plan Plan) (Result, error) {
	if err := plan.Mapping.RequireComplete(inputsOf(plan.Tracks)); err != nil {
		return Result{}, fault.Wrap(fault.StageMap, "mp4", fault.OpMapping, -1, "track mapping is not usable", err)
	}
	md, err := collectMedia(plan)
	if err != nil {
		return Result{}, err
	}
	ts := plan.movieTimescale()
	emsgBoxes := buildEmsgBoxes(plan.Events, ts)
	chapterUdta := buildChapterUdta(plan.Events, ts)
	ftyp := buildFtyp(plan.MajorBrand)

	// stco entries are fixed width, so the moov size does not depend on the
	// offset values; build once to learn the size, then relocate and rebuild.
	moov := buildMoov(plan, md, chapterUdta, ts)
	emsgSize := 0
	for _, eb := range emsgBoxes {
		emsgSize += len(eb)
	}
	md.relocate(int64(len(ftyp)+len(moov)+emsgSize) + 16) // large-size mdat header
	moov = buildMoov(plan, md, chapterUdta, ts)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	for _, eb := range emsgBoxes {
		out.Write(eb)
	}
	writeMdatHeader(&out, int64(len(md.payload)))
	out.Write(md.payload)
	sum := sha256.Sum256(md.payload)
	if _, err := w.Write(out.Bytes()); err != nil {
		return Result{}, fault.Wrap(fault.StageRemux, "mp4", fault.OpWrite, -1, "write output", err)
	}
	return Result{
		Bytes:         int64(out.Len()),
		PayloadSHA256: fmt.Sprintf("%x", sum),
		TrackCount:    len(plan.Tracks),
		EventCount:    len(plan.Events.Events),
		PayloadBytes:  int64(len(md.payload)),
	}, nil
}

func inputsOf(tracks []demux.Track) []track.Input {
	out := make([]track.Input, len(tracks))
	for i, tr := range tracks {
		out[i] = track.Input{ID: tr.ID, Kind: tr.Kind, Timescale: tr.Timescale, Codec: tr.Codec}
	}
	return out
}

func collectMedia(plan Plan) (*media, error) {
	md := &media{
		chunks:    make(map[uint32][]uint32),
		sizes:     make(map[uint32][]uint32),
		trackByID: make(map[uint32]demux.Track),
	}
	for _, tr := range plan.Tracks {
		mapped, ok := plan.Mapping.Lookup(tr.ID)
		if !ok {
			return nil, fault.New(fault.StageMap, "mp4", fault.OpMapping, -1, "track has no mapping entry")
		}
		id := mapped.OutputID
		md.order = append(md.order, id)
		mappedTrack := tr
		mappedTrack.ID = id
		md.trackByID[id] = mappedTrack
		for _, s := range tr.Samples {
			data, err := sampleBytes(plan.Src, s)
			if err != nil {
				return nil, err
			}
			md.payload = append(md.payload, data...)
			md.sizes[id] = append(md.sizes[id], s.Size)
			md.chunks[id] = append(md.chunks[id], 0)
		}
	}
	return md, nil
}

func sampleBytes(src []byte, s demux.Sample) ([]byte, error) {
	if s.Data != nil {
		if uint32(len(s.Data)) != s.Size {
			return nil, fault.New(fault.StageRemux, "mp4", fault.OpTruncated, s.Offset,
				"sample size does not match its in-memory data")
		}
		return s.Data, nil
	}
	if s.Offset < 0 || s.Offset+int64(s.Size) > int64(len(src)) {
		return nil, fault.New(fault.StageRemux, "mp4", fault.OpTruncated, s.Offset,
			"sample lies outside the input bytes")
	}
	return src[s.Offset : s.Offset+int64(s.Size)], nil
}

func writeMdatHeader(w io.Writer, payloadLen int64) {
	hdr := make([]byte, 16)
	binary.BigEndian.PutUint32(hdr, 1)
	copy(hdr[4:], "mdat")
	binary.BigEndian.PutUint64(hdr[8:], uint64(payloadLen+16))
	w.Write(hdr)
}

// eventTime converts an event to movie timescale ticks exactly.  Events whose
// time cannot be represented exactly are rejected rather than rounded.
func eventTime(t clock.Time, timescale uint32) (int64, error) {
	ticks, exact, err := t.Rescale(timescale)
	if err != nil {
		return 0, err
	}
	if !exact {
		return 0, fmt.Errorf("remux: event time %s is not exact on timescale %d", t, timescale)
	}
	return ticks, nil
}
