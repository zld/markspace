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
	// Paths are rooted at the trak box because ReadBoxStructureFromInternal
	// reports the full relative path (the box itself is the first element).
	paths := []mp4.BoxPath{
		boxPath(mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()),
		boxPath(mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()),
		boxPath(mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()),
		boxPath(mp4.BoxTypeTrak(), mp4.BoxTypeEdts(), mp4.BoxTypeElst()),
		// stbl is deliberately fetched header-first: a cut stbl makes go-mp4's
		// recursive payload unmarshal fail before any sample context exists.
		// The tolerant reader below walks the readable children itself.
		boxPath(mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl()),
	}
	var (
		trackID   uint32
		timescale uint32
		handler   string
		stblInfo  *mp4.BoxInfo
		mediaTime int64 // elst media_time, track ticks
	)
	if err := s.walkTrakMeta(trak, paths, func(typ mp4.BoxType, bi mp4.BoxInfo, payload mp4.IBox) {
		switch p := payload.(type) {
		case *mp4.Tkhd:
			trackID = p.TrackID
		case *mp4.Mdhd:
			timescale = p.Timescale
		case *mp4.Hdlr:
			handler = string(p.HandlerType[:])
		case *mp4.Elst:
			// A non-empty edit list with a non-negative media_time offsets the
			// raw sample timeline; this is how a node's absolute clock (which
			// may sit near the 32-bit boundary) is carried in the container.
			if len(p.Entries) > 0 {
				if p.Version == 1 {
					mediaTime = p.Entries[0].MediaTimeV1
				} else {
					mediaTime = int64(p.Entries[0].MediaTimeV0)
				}
			}
		}
		if typ == mp4.BoxTypeStbl() {
			info := bi
			stblInfo = &info
		}
	}); err != nil {
		return TrackTable{}, clock.NewError(clock.ErrTruncatedBox, err.Error())
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
		if ce, ok := err.(*clock.Error); ok && ce.TrackID == 0 {
			return TrackTable{}, ce.WithPosition(ce.FileOffset, trackID, ce.SampleIndex)
		}
		return TrackTable{}, err
	}
	return TrackTable{TrackID: trackID, Timescale: timescale, Handler: handler, Entries: entries}, nil
}

// sampleIdxCoverage returns how many leading samples the (possibly truncated)
// table can locate: the intersection of readable stsz sizes, readable chunk
// offsets, and the stts entries. Used to decide whether a cut table is wholly
// unusable or still yields a usable prefix plus one localized boundary error.
func sampleIdxCoverage(raw rawTable, runs []chunkRun, count int) int {
	sizes := len(raw.stsz)
	if count < sizes {
		sizes = count
	}
	locatable := 0
	for _, run := range runs {
		last := run.lastChunk
		if int(last) > len(raw.co) {
			last = uint32(len(raw.co))
		}
		if last <= run.firstChunk {
			break
		}
		locatable += int(last-run.firstChunk) * int(run.samples)
		if locatable >= sizes {
			return sizes
		}
	}
	if locatable < sizes {
		return locatable
	}
	return sizes
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

// walkTrakMeta walks the trak subtree and hands each matched box's decoded
// payload to visit. Matching is by box path suffix so nested boxes (mdhd inside
// mdia, stbl inside minf) all arrive once. Payloads that cannot be decoded are
// passed as nil rather than aborting: structural damage is localized by the
// caller's tolerant sample-table reader.
func (s *Scanner) walkTrakMeta(trak *mp4.BoxInfo, paths []mp4.BoxPath,
	visit func(typ mp4.BoxType, bi mp4.BoxInfo, payload mp4.IBox)) error {
	_, err := mp4.ReadBoxStructureFromInternal(s.f, trak, func(h *mp4.ReadHandle) (interface{}, error) {
		// Path is relative to the trak box, so [mdia, mdhd] matches the mdhd
		// inside this track's mdia.
		for _, want := range paths {
			if len(want) != len(h.Path) {
				continue
			}
			matched := true
			for i := range want {
				if want[i] != h.Path[i] {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			bi := h.BoxInfo
			payload, _, perr := h.ReadPayload()
			if perr != nil {
				payload = nil
			}
			visit(h.BoxInfo.Type, bi, payload)
			return nil, nil
		}
		// Descend into containers only; leaves outside our paths are ignored.
		switch h.BoxInfo.Type {
		case mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeEdts():
			if _, err := h.Expand(); err != nil {
				return nil, nil // damage surfaces in the tolerant reader
			}
		}
		return nil, nil
	})
	return err
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
