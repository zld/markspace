package container

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"

	"github.com/abema/go-mp4"

	"mediatimeline/internal/clock"
)

// Scanner parses one MP4/MOV file into per-track sample tables.
type Scanner struct {
	path string
	f    *os.File
}

// Open opens the container read-only.
func Open(path string) (*Scanner, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Scanner{path: path, f: f}, nil
}

// Close releases the underlying file.
func (s *Scanner) Close() error { return s.f.Close() }

// boxPath is shorthand for an mp4 extraction path.
func boxPath(types ...mp4.BoxType) mp4.BoxPath { return mp4.BoxPath(types) }

// ScanTracks extracts every trak and builds its normalized sample table.
func (s *Scanner) ScanTracks() ([]TrackTable, error) {
	fi, err := s.f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := fi.Size()

	traks, err := mp4.ExtractBoxesWithPayload(s.f, nil, []mp4.BoxPath{
		boxPath(mp4.BoxTypeMoov(), mp4.BoxTypeTrak()),
	})
	if err != nil {
		return nil, clock.NewError(clock.ErrTruncatedBox, "moov/trak: "+err.Error())
	}
	if len(traks) == 0 {
		return nil, clock.NewError(clock.ErrTruncatedBox, "no trak boxes found")
	}

	var out []TrackTable
	for ti, trak := range traks {
		tbl, err := s.scanTrak(&trak.Info, fileSize)
		if err != nil {
			// Attach a one-based track ordinal so structural damage is still
			// localizable even when the underlying box parser has no sample
			// context. Sample index remains -1 for whole-table failures.
			if ce, ok := err.(*clock.Error); ok && ce.TrackID == 0 {
				return nil, ce.WithPosition(ce.FileOffset, uint32(ti+1), ce.SampleIndex)
			}
			return nil, err
		}
		out = append(out, tbl)
	}
	return out, nil
}

// scanTrak decodes one trak subtree.
func (s *Scanner) scanTrak(trak *mp4.BoxInfo, fileSize int64) (TrackTable, error) {
	paths := []mp4.BoxPath{
		boxPath(mp4.BoxTypeTkhd()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl()),
		boxPath(mp4.BoxTypeEdts(), mp4.BoxTypeElst()),
	}
	boxes, err := mp4.ExtractBoxesWithPayload(s.f, trak, paths)
	if err != nil {
		return TrackTable{}, clock.NewError(clock.ErrTruncatedBox, err.Error())
	}
	var (
		trackID   uint32
		timescale uint32
		handler   string
		stblInfo  *mp4.BoxInfo
		mediaTime int64 // elst media_time, track ticks
	)
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Tkhd:
			trackID = p.TrackID
		case *mp4.Mdhd:
			timescale = p.Timescale
		case *mp4.Hdlr:
			handler = string(p.HandlerType[:])
		case *mp4.Stbl:
			stblInfo = &b.Info
		case *mp4.Elst:
			// A non-empty edit list offsets the raw sample timeline; this is
			// how a node's absolute clock (possibly near the 32-bit boundary)
			// is carried in the container.
			if len(p.Entries) > 0 {
				if p.Version == 1 {
					mediaTime = p.Entries[0].MediaTimeV1
				} else {
					mediaTime = int64(p.Entries[0].MediaTimeV0)
				}
			}
		}
	}
	if timescale == 0 || stblInfo == nil {
		return TrackTable{}, clock.NewError(clock.ErrTruncatedBox,
			"track missing mdhd timescale or stbl")
	}
	mc, err := clock.NewMediaClock(timescale)
	if err != nil {
		return TrackTable{}, err
	}
	entries, err := s.buildSamples(stblInfo, trackID, timescale, mc, fileSize, mediaTime)
	if err != nil {
		return TrackTable{}, err
	}
	return TrackTable{TrackID: trackID, Timescale: timescale, Handler: handler, Entries: entries}, nil
}

// channelsFor reads channel_count from the first audio sample entry inside the
// stbl's stsd. Payloads are raw PCM here, so the decoder never inspects the
// entry; only the frame width is needed for content accounting. A missing or
// malformed entry falls back to stereo, matching the fixture generator.
func (s *Scanner) channelsFor(stbl *mp4.BoxInfo) uint16 {
	var n uint16 = 2
	_, _ = mp4.ReadBoxStructureFromInternal(s.f, stbl, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type == mp4.BoxTypeStsd() && len(h.Path) == 2 {
			// stsd payload: FullBox(4) + entry_count(4) + sample entry. Inside
			// an audio sample entry channel_count sits 16 bytes into the entry.
			buf := make([]byte, 24)
			if _, err := s.f.ReadAt(buf, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize)); err == nil {
				if c := binary.BigEndian.Uint16(buf[8+8:]); c > 0 {
					n = c
				}
			}
		}
		if h.BoxInfo.Type == mp4.BoxTypeStbl() {
			_, _ = h.Expand()
		}
		return nil, nil
	})
	return n
}

// rawTable holds the decoded stbl children.
type rawTable struct {
	stts []sttsEntry
	stsc []stscEntry
	stsz []uint32 // per-sample sizes; empty means uniformSize applies
	co   []uint64 // chunk offsets (stco widened to 64 bit)
}

// readStbl decodes the sample-table boxes via go-mp4 payload structs.
func (s *Scanner) readStbl(stbl *mp4.BoxInfo) (rawTable, int, error) {
	paths := []mp4.BoxPath{
		boxPath(mp4.BoxTypeStts()),
		boxPath(mp4.BoxTypeStsc()),
		boxPath(mp4.BoxTypeStsz()),
		boxPath(mp4.BoxTypeStco()),
		boxPath(mp4.BoxTypeCo64()),
	}
	boxes, err := mp4.ExtractBoxesWithPayload(s.f, stbl, paths)
	if err != nil {
		return rawTable{}, 0, clock.NewError(clock.ErrTruncatedBox, err.Error())
	}
	var raw rawTable
	var uniform uint32
	sampleCount := -1
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Stts:
			for _, e := range p.Entries {
				raw.stts = append(raw.stts, sttsEntry{e.SampleCount, e.SampleDelta})
			}
		case *mp4.Stsc:
			for _, e := range p.Entries {
				raw.stsc = append(raw.stsc, stscEntry{e.FirstChunk, e.SamplesPerChunk})
			}
		case *mp4.Stsz:
			sampleCount = int(p.SampleCount)
			uniform = p.SampleSize
			raw.stsz = append(raw.stsz, p.EntrySize...)
		case *mp4.Stco:
			for _, off := range p.ChunkOffset {
				raw.co = append(raw.co, uint64(off))
			}
		case *mp4.Co64:
			raw.co = append(raw.co, p.ChunkOffset...)
		}
	}
	if sampleCount < 0 || len(raw.stts) == 0 || len(raw.stsc) == 0 || len(raw.co) == 0 {
		return raw, 0, clock.NewError(clock.ErrTruncatedBox, "incomplete stbl sample table")
	}
	if uniform != 0 {
		raw.stsz = make([]uint32, sampleCount)
		for i := range raw.stsz {
			raw.stsz[i] = uniform
		}
	}
	if len(raw.stsz) != sampleCount {
		return raw, 0, clock.NewError(clock.ErrTruncatedBox,
			fmt.Sprintf("stsz size entries=%d want %d", len(raw.stsz), sampleCount))
	}
	return raw, sampleCount, nil
}

// buildSamples materializes every sample with offset, timing and CRC, enforcing
// all structural validation at the point each sample is touched.
func (s *Scanner) buildSamples(stbl *mp4.BoxInfo, trackID, timescale uint32,
	mc *clock.MediaClock, fileSize int64, mediaTime int64) ([]SampleEvent, error) {
	raw, count, err := s.readStbl(stbl)
	if err != nil {
		return nil, err
	}
	dts, dur, err := decodeStts(raw.stts, count)
	if err != nil {
		return nil, err
	}
	runs, err := expandStsc(raw.stsc, len(raw.co))
	if err != nil {
		return nil, err
	}

	entries := make([]SampleEvent, 0, count)
	sampleIdx := 0
	var contentFrames uint64
	channels := int(s.channelsFor(stbl))
	crcTable := crc32.MakeTable(crc32.IEEE)
	buf := make([]byte, 0, 4096)
	for _, run := range runs {
		for chunk := run.firstChunk; chunk < run.lastChunk; chunk++ {
			if int(chunk) >= len(raw.co) {
				return nil, clock.NewError(clock.ErrTruncatedBox,
					"stsc references chunk beyond stco/co64 table").
					WithPosition(-1, trackID, int64(sampleIdx))
			}
			offset := int64(raw.co[chunk])
			for j := uint32(0); j < run.samples; j++ {
				if sampleIdx >= count {
					return nil, clock.NewError(clock.ErrTruncatedBox,
						"stsc/stsz sample count mismatch").
						WithPosition(offset, trackID, int64(sampleIdx))
				}
				size := int64(raw.stsz[sampleIdx])
				if offset < 0 || offset+size > fileSize {
					return nil, clock.NewError(clock.ErrIllegalOffset,
						fmt.Sprintf("sample [%d,%d) outside file of %d bytes", offset, offset+size, fileSize)).
						WithPosition(offset, trackID, int64(sampleIdx))
				}
				if cap(buf) < int(size) {
					buf = make([]byte, size)
				}
				buf = buf[:size]
				if _, err := s.f.ReadAt(buf, offset); err != nil {
					return nil, clock.NewError(clock.ErrIllegalOffset, err.Error()).
						WithPosition(offset, trackID, int64(sampleIdx))
				}
				// Presentation time excludes the edit-list offset; raw ticks
				// retain the absolute node-clock value for source location.
				posTicks := dts[sampleIdx]
				rawTicks := posTicks + uint64(mediaTime)
				frames := uint64(size) / (2 * uint64(channels))
				entries = append(entries, SampleEvent{
					TrackID:      trackID,
					SampleIndex:  int64(sampleIdx),
					DTSNanos:     mc.ToNanos(posTicks),
					PTSNanos:     mc.ToNanos(posTicks), // CTTS added later when ctts exists
					DTSRaw:       rawTicks,
					PTSRaw:       rawTicks,
					DurationRaw:  dur[sampleIdx],
					ContentFrame: contentFrames,
					Timescale:    timescale,
					FileOffset:   offset,
					PayloadSize:  size,
					PayloadCRC32: crc32.Checksum(buf, crcTable),
				})
				contentFrames += frames
				offset += size
				sampleIdx++
			}
		}
	}
	if sampleIdx != count {
		return nil, clock.NewError(clock.ErrTruncatedBox,
			fmt.Sprintf("materialized %d samples, stsz declares %d", sampleIdx, count))
	}
	return entries, nil
}

// AttachTrustedCRC annotates events with authoritative CRCs from a known-good
// copy of the same media, keyed by (track, sample index). This models an ingest
// manifest and lets the pipeline detect corruption introduced after delivery.
func AttachTrustedCRC(events []SampleEvent, trusted []SampleEvent) {
	key := func(e SampleEvent) uint64 { return uint64(e.TrackID)<<32 | uint64(uint32(e.SampleIndex)) }
	ref := make(map[uint64]uint32, len(trusted))
	for _, e := range trusted {
		ref[key(e)] = e.PayloadCRC32
	}
	for i := range events {
		if c, ok := ref[key(events[i])]; ok {
			events[i].TrustedCRC32 = c
		}
	}
}

// u32/u64 big-endian helpers used by the fixture generator and corruption tool.
func u32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func u64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }
