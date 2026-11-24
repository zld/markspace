// Package mp4 scans PCM-in-MP4 containers with go-mp4, rebuilds missing or
// damaged sample indexes, and turns the surviving sample boundaries into a
// confirmed set of presentation intervals.  Tables are read on demand and
// media payloads are streamed through a bounded window, so long assets never
// sit in memory.
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	gomp4 "github.com/abema/go-mp4"

	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
)

// Limits enforced while scanning.
const (
	MaxScanBytes       = 1 << 30
	MaxSamplesPerTrack = 4_000_000
	ReadWindowBytes    = 64 << 10
)

// Sample is one indexed sample with its confirmed or suspected extent.
type Sample struct {
	Index     int   // zero-based sample number
	Offset    int64 // absolute byte offset of the payload
	Size      int64 // declared size in bytes
	Confirmed bool  // boundary consistent with tables and inside the file
	Delta     int64 // duration in media ticks from stts
	MediaTick int64 // cumulative media tick at sample start
}

// Chunk groups consecutive samples sharing one data-offset entry.
type Chunk struct {
	Index   int
	Offset  int64
	Size    int64
	Samples []int
}

// EditEntry mirrors one elst record in decoded form.
type EditEntry struct {
	MediaTime int64 // media ticks, -1 for an empty edit
	Duration  int64 // movie ticks
}

// TrackInfo captures the container facts needed downstream.
type TrackInfo struct {
	ID             uint32
	Handler        string
	MediaTimescale uint32
	MediaDuration  int64
	Channels       int
	SampleSize     int
	SampleRate     int
	Codec          string
	Layout         string
	ConstantSize   int64
	Samples        []Sample
	Chunks         []Chunk
	Edits          []EditEntry
	SyncSamples    []uint32
	Sparse         bool
	MediaTicks     int64 // total media duration from stts
}

// TopBox is one top-level box extent.
type TopBox struct {
	Type   string
	Offset int64
	Size   int64
}

// ScanResult is the outcome of scanning one file.
type ScanResult struct {
	Path           string
	Bytes          int64
	Declared       int64
	MovieTimescale uint32
	Tracks         []TrackInfo
	Timeline       *clock.Timeline
	TopLevel       []TopBox
	Damaged        []fault.Fault
	IndexMissing   bool
	Truncated      bool
	CorruptOffset  int64 // -1 when every boundary was confirmed
	MDATStart      int64
	// ScanPin is the byte offset of the first unconfirmed sample boundary, or
	// 0 when every boundary was confirmed.  It is deterministic and used for
	// repeated-run and degraded-plan checks.
	ScanPin int64
}

// AddFault records a fault from scanning and tracks the earliest byte offset
// where a boundary could no longer be confirmed.
func (r *ScanResult) AddFault(f fault.Fault) {
	r.Damaged = append(r.Damaged, f)
	if f.OffsetKind == fault.OffsetByte && f.Recovery == fault.Recoverable {
		if r.CorruptOffset < 0 || f.Offset < r.CorruptOffset {
			r.CorruptOffset = f.Offset
		}
	}
}

// Scanner scans one container and never writes to it.
type Scanner struct {
	MaxBytes    int64
	MaxSamples  int64
	ChunkWindow int64
	// StartTick pins the first presentation tick; defaults to zero.
	StartTick int64
}

func (s *Scanner) limits() (int64, int64, int64) {
	maxBytes, maxSamples, window := int64(MaxScanBytes), int64(MaxSamplesPerTrack), int64(ReadWindowBytes)
	if s.MaxBytes > 0 {
		maxBytes = s.MaxBytes
	}
	if s.MaxSamples > 0 {
		maxSamples = s.MaxSamples
	}
	if s.ChunkWindow > 0 {
		window = s.ChunkWindow
	}
	return maxBytes, maxSamples, window
}

// Scan opens path read-only and scans it.
func (s *Scanner) Scan(path string) (*ScanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Fatal(fault.ClassInput, "open", err.Error()).Because(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fault.Fatal(fault.ClassInput, "stat", err.Error()).Because(err)
	}
	return s.ScanReader(f, st.Size(), path)
}

// ScanReader scans an open container.  readable is the physically available
// byte count and may be smaller than the box sizes declare.
func (s *Scanner) ScanReader(r io.ReadSeeker, readable int64, path string) (*ScanResult, error) {
	maxBytes, maxSamples, window := s.limits()
	if readable > maxBytes {
		return nil, fault.Fatal(fault.ClassLimit, "file-size", fmt.Sprintf("%s is %d bytes, cap is %d", path, readable, maxBytes))
	}
	_ = window
	res := &ScanResult{Path: path, Bytes: readable, CorruptOffset: -1, MDATStart: -1}

	top, declared, truncated, _, err := scanTopLevel(r, readable)
	if err != nil {
		return nil, fault.Fatal(fault.ClassContainer, "top-level", err.Error()).Because(err)
	}
	res.TopLevel = top
	res.Declared = declared
	res.Truncated = truncated
	for _, b := range top {
		if b.Type == "mdat" {
			res.MDATStart = b.Offset + 8
			break
		}
	}
	if truncated {
		res.AddFault(fault.New(fault.ClassContainer, "truncated",
			fmt.Sprintf("file ends at %d but boxes declare %d bytes", readable, declared)).AtByte(declared))
	}

	st, err := probeStructure(r)
	if err != nil {
		return nil, fault.Fatal(fault.ClassContainer, "moov", err.Error()).Because(err)
	}
	res.MovieTimescale = st.movieTimescale

	for _, tr := range st.tracks {
		ti, fs, err := buildTrack(tr, readable, maxSamples, res.MDATStart)
		if err != nil {
			return nil, err
		}
		res.Tracks = append(res.Tracks, ti)
		for _, f := range fs {
			res.AddFault(f)
			if f.Code == "index-missing" {
				res.IndexMissing = true
			}
		}
	}
	tl, err := BuildTimeline(res, s.StartTick)
	if err != nil {
		return nil, err
	}
	res.Timeline = tl
	return res, nil
}

// scanTopLevel walks top-level box headers only.  It stops at the first box
// whose declared end exceeds the readable bytes and reports that offset.
func scanTopLevel(r io.ReadSeeker, readable int64) (boxes []TopBox, declared int64, truncated bool, firstBad int64, err error) {
	firstBad = -1
	var off int64
	hdr := make([]byte, 16)
	for off+8 <= readable {
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return nil, 0, false, -1, err
		}
		if _, err := io.ReadFull(r, hdr[:8]); err != nil {
			return nil, 0, false, -1, err
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		switch {
		case size == 1:
			if off+16 > readable {
				return boxes, off + 8, true, off, nil
			}
			if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
				return nil, 0, false, -1, err
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
		case size == 0:
			size = readable - off
		}
		if size < 8 {
			return boxes, off + size, false, off, fmt.Errorf("box %q at %d has invalid size %d", typ, off, size)
		}
		boxes = append(boxes, TopBox{Type: typ, Offset: off, Size: size})
		if off+size > readable {
			return boxes, off + size, true, readable, nil
		}
		declared = off + size
		off += size
	}
	return boxes, declared, false, firstBad, nil
}

// --- go-mp4 structural extraction -----------------------------------------

type structTracks struct {
	movieTimescale uint32
	tracks         []structTrack
}

type structTrack struct {
	id             uint32
	handler        string
	mediaTimescale uint32
	mediaDuration  int64
	channels       int
	sampleSizeBits int
	sampleRate     int
	sampleType     string
	stts           []gomp4.SttsEntry
	stsc           []gomp4.StscEntry
	stszConstant   uint32
	stszCount      uint32
	stszSizes      []uint32
	stco           []uint32
	co64           []uint64
	edits          []EditEntry
	syncSamples    []uint32
}

func probeStructure(r io.ReadSeeker) (*structTracks, error) {
	out := &structTracks{}
	mvhd, err := gomp4.ExtractBoxWithPayload(r, nil, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeMvhd()})
	if err != nil {
		return nil, err
	}
	if len(mvhd) == 0 {
		return nil, errors.New("mvhd box not found")
	}
	out.movieTimescale = mvhd[0].Payload.(*gomp4.Mvhd).Timescale

	traks, err := gomp4.ExtractBox(r, nil, gomp4.BoxPath{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak()})
	if err != nil {
		return nil, err
	}
	if len(traks) == 0 {
		return nil, errors.New("no trak boxes found")
	}
	for _, trak := range traks {
		st, err := probeTrack(r, trak)
		if err != nil {
			return nil, err
		}
		out.tracks = append(out.tracks, *st)
	}
	return out, nil
}

func probeTrack(r io.ReadSeeker, trak *gomp4.BoxInfo) (*structTrack, error) {
	st := &structTrack{}
	if boxes, err := gomp4.ExtractBoxWithPayload(r, trak, gomp4.BoxPath{gomp4.BoxTypeTkhd()}); err == nil && len(boxes) > 0 {
		st.id = boxes[0].Payload.(*gomp4.Tkhd).TrackID
	}
	if boxes, err := gomp4.ExtractBoxWithPayload(r, trak, gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMdhd()}); err == nil && len(boxes) > 0 {
		m := boxes[0].Payload.(*gomp4.Mdhd)
		st.mediaTimescale = m.Timescale
		st.mediaDuration = int64(m.GetDuration())
	}
	if boxes, err := gomp4.ExtractBoxWithPayload(r, trak, gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeHdlr()}); err == nil && len(boxes) > 0 {
		h := boxes[0].Payload.(*gomp4.Hdlr)
		st.handler = string(h.HandlerType[:])
	}
	for _, bt := range []gomp4.BoxType{gomp4.BoxTypeIpcm(), gomp4.BoxTypeFpcm(), gomp4.BoxTypeMp4a()} {
		boxes, err := gomp4.ExtractBoxWithPayload(r, trak, gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsd(), bt})
		if err != nil || len(boxes) == 0 {
			continue
		}
		st.sampleType = bt.String()
		ase := boxes[0].Payload.(*gomp4.AudioSampleEntry)
		st.channels = int(ase.ChannelCount)
		st.sampleSizeBits = int(ase.SampleSize)
		st.sampleRate = int(ase.SampleRate >> 16)
		break
	}
	if st.sampleType == "ipcm" {
		if boxes, err := gomp4.ExtractBoxWithPayload(r, trak, gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsd(), gomp4.BoxTypeIpcm(), gomp4.BoxTypePcmC()}); err == nil && len(boxes) > 0 {
			p := boxes[0].Payload.(*gomp4.PcmC)
			st.sampleSizeBits = int(p.PCMSampleSize)
			if p.FormatFlags&0x1 == 0 {
				st.sampleType = "ipcm-be"
			}
		}
	}
	table := func(path gomp4.BoxPath, load func(gomp4.IBox)) {
		boxes, err := gomp4.ExtractBoxWithPayload(r, trak, path)
		if err != nil || len(boxes) == 0 {
			return
		}
		load(boxes[0].Payload)
	}
	table(gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStts()}, func(b gomp4.IBox) { st.stts = b.(*gomp4.Stts).Entries })
	table(gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsc()}, func(b gomp4.IBox) { st.stsc = b.(*gomp4.Stsc).Entries })
	table(gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsz()}, func(b gomp4.IBox) {
		s := b.(*gomp4.Stsz)
		st.stszConstant, st.stszCount, st.stszSizes = s.SampleSize, s.SampleCount, s.EntrySize
	})
	table(gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStco()}, func(b gomp4.IBox) { st.stco = b.(*gomp4.Stco).ChunkOffset })
	table(gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeCo64()}, func(b gomp4.IBox) { st.co64 = b.(*gomp4.Co64).ChunkOffset })
	table(gomp4.BoxPath{gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStss()}, func(b gomp4.IBox) { st.syncSamples = b.(*gomp4.Stss).SampleNumber })
	if boxes, err := gomp4.ExtractBoxWithPayload(r, trak, gomp4.BoxPath{gomp4.BoxTypeEdts(), gomp4.BoxTypeElst()}); err == nil && len(boxes) > 0 {
		el := boxes[0].Payload.(*gomp4.Elst)
		for i := uint32(0); i < el.EntryCount && int(i) < len(el.Entries); i++ {
			e := el.Entries[i]
			mt, dur := int64(e.MediaTimeV0), int64(e.SegmentDurationV0)
			if el.GetVersion() != 0 {
				mt, dur = e.MediaTimeV1, int64(e.SegmentDurationV1)
			}
			st.edits = append(st.edits, EditEntry{MediaTime: mt, Duration: dur})
		}
	}
	return st, nil
}

// --- shared helpers -----------------------------------------------------

func layoutForChannels(c int) string {
	switch c {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	default:
		return fmt.Sprintf("unknown-%d", c)
	}
}

func sortFaults(fs []fault.Fault) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Offset != fs[j].Offset {
			return fs[i].Offset < fs[j].Offset
		}
		return fs[i].Code < fs[j].Code
	})
}

// PayloadReader streams one sample's payload through a bounded window so the
// decoder never allocates the whole media buffer.
type PayloadReader struct {
	r      io.ReaderAt
	offset int64
	size   int64
	window int64
	pos    int64
}

// NewPayloadReader builds a reader over the sample's byte extent.
func NewPayloadReader(r io.ReaderAt, s Sample, window int64) *PayloadReader {
	if window <= 0 {
		window = ReadWindowBytes
	}
	return &PayloadReader{r: r, offset: s.Offset, size: s.Size, window: window}
}

// Read implements io.Reader without exceeding the sample's extent.
func (p *PayloadReader) Read(dst []byte) (int, error) {
	if p.pos >= p.size {
		return 0, io.EOF
	}
	want := int64(len(dst))
	if remaining := p.size - p.pos; remaining < want {
		want = remaining
	}
	n, err := p.r.ReadAt(dst[:want], p.offset+p.pos)
	p.pos += int64(n)
	if err != nil && n == 0 {
		return 0, err
	}
	return n, nil
}

// ReadFull fills dst or reports io.ErrUnexpectedEOF.
func (p *PayloadReader) ReadFull(dst []byte) error {
	_, err := io.ReadFull(p, dst)
	return err
}
