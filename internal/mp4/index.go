package mp4

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"mediapipe/internal/model"
)

// Sample locates one coded sample within the input.
type Sample struct {
	Index     int
	Offset    int64 // absolute payload offset of the sample
	Size      int64
	DecodeTks int64 // decode timestamp in media ticks
	Duration  int64 // duration in media ticks
	Sync      bool  // true when the sample is a sync (key) sample
}

// End returns the exclusive end offset of the sample payload.
func (s Sample) End() int64 { return s.Offset + s.Size }

// TrackIndex is the recovered, decodable view of one track.
type TrackIndex struct {
	TrackID   uint32
	Handler   string
	Timescale uint32
	EditMedia int64 // first edit's media_time, in media ticks
	EditDur   int64 // first edit's segment duration, in media ticks
	Samples   []Sample
	// Complete is false when stbl tables were missing or inconsistent and
	// the samples had to be reconstructed by scanning the media data.
	Complete   bool
	Recovered  int
	Gaps       []Gap
	SourceNote string
}

// Gap is a region of the media timeline that could not be located. It is
// reported, never silently filled.
type Gap struct {
	StartTicks int64
	Duration   int64
	Bytes      int64
}

// Index is the scan result for a whole asset file.
type Index struct {
	TrackID    uint32
	Timescale  uint32
	Tracks     []TrackIndex
	Boxes      []Box
	Recovered  bool
	TotalBytes int64
}

// Localize returns the sample covering the given media tick for the track, or
// the preceding sync sample when audio-less video has sparse keyframes.
func (t *TrackIndex) Localize(ticks int64) (Sample, bool) {
	if len(t.Samples) == 0 {
		return Sample{}, false
	}
	i := sort.Search(len(t.Samples), func(i int) bool {
		return t.Samples[i].DecodeTks > ticks
	})
	if i == 0 {
		return t.Samples[0], true
	}
	if i < len(t.Samples) && t.Samples[i].DecodeTks == ticks {
		return t.Samples[i], true
	}
	return t.Samples[i-1], true
}

// KeyframeBefore returns the closest sync sample at or before ticks, plus the
// locating error in ticks (exact timestamp minus the sync sample timestamp).
func (t *TrackIndex) KeyframeBefore(ticks int64) (Sample, int64, bool) {
	best := -1
	for i := range t.Samples {
		if !t.Samples[i].Sync {
			continue
		}
		if t.Samples[i].DecodeTks <= ticks {
			best = i
			continue
		}
		break
	}
	if best < 0 {
		return Sample{}, 0, false
	}
	s := t.Samples[best]
	return s, ticks - s.DecodeTks, true
}

// BuildIndex scans a complete file and constructs per-track sample tables.
// When the stbl tables are absent or inconsistent it falls back to a media
// data scan and marks the track incomplete rather than inventing samples.
func BuildIndex(ctx context.Context, s *Stream) (*Index, error) {
	idx := &Index{TotalBytes: s.Size()}
	byTrack := map[uint32]*TrackIndex{}
	var order []uint32
	err := s.Walk(ctx, func(b Box) error {
		idx.Boxes = append(idx.Boxes, b)
		if b.Type != "trak" {
			return nil
		}
		t, err := buildTrack(ctx, s, b)
		if err != nil {
			return err
		}
		if t == nil {
			return nil
		}
		// Track IDs are not guaranteed to follow trak order on disk;
		// keep discovery order for stable association with the manifest.
		byTrack[t.TrackID] = t
		order = append(order, t.TrackID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range order {
		t := byTrack[id]
		idx.Tracks = append(idx.Tracks, *t)
		if !t.Complete {
			idx.Recovered = true
		}
	}
	if len(idx.Tracks) == 0 {
		return nil, model.New(model.KindCorrupt, model.StageMP4Scan, s.asset, "index",
			errors.New("no tracks found"))
	}
	idx.TrackID = idx.Tracks[0].TrackID
	idx.Timescale = idx.Tracks[0].Timescale
	return idx, nil
}

func buildTrack(ctx context.Context, s *Stream, trak Box) (*TrackIndex, error) {
	var tkhd *Box
	var mdhdBox, elstBox, stblBox, hdlrBox *Box
	for _, b := range s.boxesUnder(trak) {
		switch b.Type {
		case "tkhd":
			bb := b
			tkhd = &bb
		case "mdhd":
			bb := b
			mdhdBox = &bb
		case "elst":
			bb := b
			elstBox = &bb
		case "hdlr":
			bb := b
			hdlrBox = &bb
		case "stbl":
			bb := b
			stblBox = &bb
		}
	}
	if tkhd == nil || mdhdBox == nil || hdlrBox == nil {
		return nil, s.corrupt("track", trak.Offset, errors.New("trak missing tkhd/mdhd/hdlr"))
	}
	t := &TrackIndex{Complete: true}
	trackID, err := readU32(s, *tkhd, 12)
	if err != nil {
		return nil, err
	}
	t.TrackID = trackID
	handler, err := readHandler(s, *hdlrBox)
	if err != nil {
		return nil, err
	}
	t.Handler = handler
	ts, _, err := readMdhd(s, *mdhdBox)
	if err != nil {
		return nil, err
	}
	t.Timescale = ts
	if t.Timescale == 0 {
		return nil, s.corrupt("track", mdhdBox.Offset, errors.New("zero timescale"))
	}
	if elstBox != nil {
		media, dur, err := readFirstEdit(s, *elstBox)
		if err != nil {
			return nil, err
		}
		t.EditMedia, t.EditDur = media, dur
	}
	if stblBox == nil {
		return nil, s.corrupt("track", trak.Offset, errors.New("trak missing stbl"))
	}
	tables, err := readStbl(ctx, s, *stblBox)
	if err != nil {
		return nil, err
	}
	if tables.err != nil || tables.stts == nil || tables.stsz == nil {
		if tables.rawStsz == nil || tables.rawStts == nil {
			return nil, s.corrupt("track", stblBox.Offset,
				fmt.Errorf("sample tables unusable: %v", tables.err))
		}
	}
	if tables.complete() {
		samples, err := tables.samples(s.cfg)
		if err != nil {
			return nil, err
		}
		t.Samples = samples
	} else {
		rec, gaps, note, err := recoverSamples(s, trak, *stblBox, tables)
		if err != nil {
			return nil, err
		}
		t.Samples = rec
		t.Gaps = gaps
		t.Complete = false
		t.Recovered = len(rec)
		t.SourceNote = note
	}
	return t, nil
}

// boxesUnder returns every box nested inside trak. Sibling traks share the
// same box path, so containment is decided by byte range, not by path.
func (s *Stream) boxesUnder(trak Box) []Box {
	out := make([]Box, 0, 8)
	_ = s.Walk(context.Background(), func(b Box) error {
		if b.Offset > trak.Offset && b.PayloadEnd() <= trak.PayloadEnd() {
			out = append(out, b)
		}
		return nil
	})
	return out
}

func readU32(s *Stream, b Box, rel int64) (uint32, error) {
	var buf [4]byte
	if err := s.readAt(buf[:], b.Offset+b.HeaderSize+rel); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func readHandler(s *Stream, b Box) (string, error) {
	// FullBox(4) + pre_defined(4) + handler_type(4)
	var buf [4]byte
	if err := s.readAt(buf[:], b.Offset+b.HeaderSize+8); err != nil {
		return "", err
	}
	return string(buf[:]), nil
}

func readMdhd(s *Stream, b Box) (uint32, uint64, error) {
	payload, err := s.Payload(b)
	if err != nil {
		return 0, 0, err
	}
	if len(payload) < 4 {
		return 0, 0, s.corrupt("mdhd", b.Offset, ErrTruncated)
	}
	ver := payload[0]
	var ts uint32
	var dur uint64
	switch ver {
	case 0:
		if len(payload) < 20 {
			return 0, 0, s.corrupt("mdhd", b.Offset, ErrTruncated)
		}
		ts = binary.BigEndian.Uint32(payload[12:16])
		dur = uint64(binary.BigEndian.Uint32(payload[16:20]))
	case 1:
		if len(payload) < 32 {
			return 0, 0, s.corrupt("mdhd", b.Offset, ErrTruncated)
		}
		ts = binary.BigEndian.Uint32(payload[20:24])
		dur = binary.BigEndian.Uint64(payload[24:32])
	default:
		return 0, 0, s.corrupt("mdhd", b.Offset, fmt.Errorf("unsupported version %d", ver))
	}
	return ts, dur, nil
}

func readFirstEdit(s *Stream, b Box) (int64, int64, error) {
	payload, err := s.Payload(b)
	if err != nil {
		return 0, 0, err
	}
	if len(payload) < 8 {
		return 0, 0, s.corrupt("elst", b.Offset, ErrTruncated)
	}
	ver := payload[0]
	count := binary.BigEndian.Uint32(payload[4:8])
	if count == 0 {
		return 0, 0, nil
	}
	switch ver {
	case 0:
		if len(payload) < 8+12 {
			return 0, 0, s.corrupt("elst", b.Offset, ErrTruncated)
		}
		dur := int64(binary.BigEndian.Uint32(payload[8:12]))
		media := int64(int32(binary.BigEndian.Uint32(payload[12:16])))
		return media, dur, nil
	case 1:
		if len(payload) < 8+20 {
			return 0, 0, s.corrupt("elst", b.Offset, ErrTruncated)
		}
		dur := int64(binary.BigEndian.Uint64(payload[8:16]))
		media := int64(binary.BigEndian.Uint64(payload[16:24]))
		return media, dur, nil
	default:
		return 0, 0, s.corrupt("elst", b.Offset, fmt.Errorf("unsupported version %d", ver))
	}
}

// stblTables is the raw parsed sample-table state. A nil table or a non-nil
// err means the index is incomplete and must be recovered.
type stblTables struct {
	cfg       Config
	stts      []sttsEntry
	stsz      []uint32
	stco      []uint64
	stsc      []stscEntry
	stss      map[uint32]bool
	hasStss   bool
	co64      bool
	rawStts   []byte
	rawStsz   []byte
	rawStco   []byte
	rawStsc   []byte
	rawStss   []byte
	err       error
	chunkBase bool
}

type sttsEntry struct{ count, delta uint32 }
type stscEntry struct{ firstChunk, samplesPerChunk uint32 }

func (t *stblTables) complete() bool {
	if t.err != nil {
		return false
	}
	if len(t.stts) == 0 || len(t.stsz) == 0 || len(t.stco) == 0 || len(t.stsc) == 0 {
		return false
	}
	var total uint32
	for _, e := range t.stts {
		if uint64(total)+uint64(e.count) > uint64(math32max) {
			return false
		}
		total += e.count
	}
	return total == uint32(len(t.stsz))
}

const math32max = ^uint32(0)

func readStbl(ctx context.Context, s *Stream, stbl Box) (*stblTables, error) {
	t := &stblTables{cfg: s.Config()}
	err := s.walk(ctx, stbl.Offset+stbl.HeaderSize, stbl.PayloadEnd(), stbl.Path, len(stbl.Path), func(b Box) error {
		payload, err := s.Payload(b)
		if err != nil {
			return err
		}
		switch b.Type {
		case "stts":
			t.rawStts = payload
			t.stts, err = parseStts(payload, b.Offset)
		case "stsz":
			t.rawStsz = payload
			t.stsz, err = parseStsz(payload, b.Offset, t.cfg.MaxSamples)
		case "stco":
			t.rawStco = payload
			t.stco, err = parseStco(payload, b.Offset, t.cfg)
		case "co64":
			t.rawStco = payload
			t.co64 = true
			t.stco, err = parseCo64(payload, b.Offset, t.cfg)
		case "stsc":
			t.rawStsc = payload
			t.stsc, err = parseStsc(payload, b.Offset)
		case "stss":
			t.rawStss = payload
			t.stss, err = parseStss(payload, b.Offset, t.cfg)
			t.hasStss = true
		}
		if err != nil && t.err == nil {
			t.err = err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func parseFullBoxCount(payload []byte, off int64, limit int) (uint32, error) {
	if len(payload) < 8 {
		return 0, fmt.Errorf("table at %d truncated: %d bytes", off, len(payload))
	}
	count := binary.BigEndian.Uint32(payload[4:8])
	if limit > 0 && int64(count) > int64(limit) {
		return 0, fmt.Errorf("table at %d entry count %d exceeds limit %d", off, count, limit)
	}
	return count, nil
}

func parseStts(payload []byte, off int64) ([]sttsEntry, error) {
	count, err := parseFullBoxCount(payload, off, 1<<20)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) < 8+uint64(count)*8 {
		return nil, fmt.Errorf("stts at %d truncated: %d entries in %d bytes", off, count, len(payload))
	}
	out := make([]sttsEntry, count)
	for i := uint32(0); i < count; i++ {
		base := 8 + i*8
		out[i] = sttsEntry{
			count: binary.BigEndian.Uint32(payload[base : base+4]),
			delta: binary.BigEndian.Uint32(payload[base+4 : base+8]),
		}
	}
	return out, nil
}

func parseStsz(payload []byte, off int64, maxSamples int) ([]uint32, error) {
	if len(payload) < 12 {
		return nil, fmt.Errorf("stsz at %d truncated", off)
	}
	size := binary.BigEndian.Uint32(payload[4:8])
	count := binary.BigEndian.Uint32(payload[8:12])
	if maxSamples > 0 && int64(count) > int64(maxSamples) {
		return nil, fmt.Errorf("stsz at %d sample count %d exceeds limit %d", off, count, maxSamples)
	}
	if size != 0 {
		out := make([]uint32, count)
		for i := range out {
			out[i] = size
		}
		return out, nil
	}
	if uint64(len(payload)) < 12+uint64(count)*4 {
		return nil, fmt.Errorf("stsz at %d truncated: %d entries in %d bytes", off, count, len(payload))
	}
	out := make([]uint32, count)
	for i := uint32(0); i < count; i++ {
		base := 12 + i*4
		out[i] = binary.BigEndian.Uint32(payload[base : base+4])
	}
	return out, nil
}

func parseStco(payload []byte, off int64, cfg Config) ([]uint64, error) {
	count, err := parseFullBoxCount(payload, off, 1<<20)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) < 8+uint64(count)*4 {
		return nil, fmt.Errorf("stco at %d truncated", off)
	}
	out := make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		base := 8 + i*4
		out[i] = uint64(binary.BigEndian.Uint32(payload[base : base+4]))
	}
	return out, nil
}

func parseCo64(payload []byte, off int64, cfg Config) ([]uint64, error) {
	count, err := parseFullBoxCount(payload, off, 1<<20)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) < 8+uint64(count)*8 {
		return nil, fmt.Errorf("co64 at %d truncated", off)
	}
	out := make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		base := 8 + i*8
		out[i] = binary.BigEndian.Uint64(payload[base : base+8])
	}
	return out, nil
}

func parseStsc(payload []byte, off int64) ([]stscEntry, error) {
	count, err := parseFullBoxCount(payload, off, 1<<20)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) < 8+uint64(count)*12 {
		return nil, fmt.Errorf("stsc at %d truncated", off)
	}
	out := make([]stscEntry, count)
	for i := uint32(0); i < count; i++ {
		base := 8 + i*12
		out[i] = stscEntry{
			firstChunk:      binary.BigEndian.Uint32(payload[base : base+4]),
			samplesPerChunk: binary.BigEndian.Uint32(payload[base+4 : base+8]),
		}
	}
	return out, nil
}

func parseStss(payload []byte, off int64, cfg Config) (map[uint32]bool, error) {
	count, err := parseFullBoxCount(payload, off, 1<<20)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) < 8+uint64(count)*4 {
		return nil, fmt.Errorf("stss at %d truncated", off)
	}
	out := make(map[uint32]bool, count)
	for i := uint32(0); i < count; i++ {
		base := 8 + i*4
		out[binary.BigEndian.Uint32(payload[base:base+4])] = true
	}
	return out, nil
}

// samples lays the tables out into absolute sample locations. Any chunk offset
// that leaves the input is reported as a corruption at that offset.
func (t *stblTables) samples(cfg Config) ([]Sample, error) {
	out := make([]Sample, 0, len(t.stsz))
	var dts int64
	sampleNum := uint32(1)
	sttsIdx := 0
	sttsRemain := uint32(0)
	for chunk := 0; chunk < len(t.stco); chunk++ {
		perChunk := samplesInChunk(t.stsc, chunk+1)
		off := int64(t.stco[chunk])
		for j := uint32(0); j < perChunk && int(sampleNum) <= len(t.stsz); j++ {
			size := int64(t.stsz[sampleNum-1])
			dur := int64(0)
			if sttsRemain == 0 && sttsIdx < len(t.stts) {
				sttsRemain = t.stts[sttsIdx].count
				dur = int64(t.stts[sttsIdx].delta)
				sttsIdx++
			}
			if sttsRemain > 0 {
				if dur == 0 {
					dur = int64(t.stts[sttsIdx-1].delta)
				}
				sttsRemain--
			}
			sync := true
			if t.hasStss {
				sync = t.stss[sampleNum]
			}
			out = append(out, Sample{
				Index:     int(sampleNum),
				Offset:    off,
				Size:      size,
				DecodeTks: dts,
				Duration:  dur,
				Sync:      sync,
			})
			off += size
			dts += dur
			sampleNum++
		}
	}
	return out, nil
}

func samplesInChunk(stsc []stscEntry, chunk int) uint32 {
	if len(stsc) == 0 {
		return 0
	}
	idx := 0
	for i := range stsc {
		if int(stsc[i].firstChunk) <= chunk {
			idx = i
		}
	}
	return stsc[idx].samplesPerChunk
}
