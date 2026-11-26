package mp4

import (
	"encoding/binary"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"replayctl/internal/fault"
)

type trackBuilder struct {
	id        uint32
	kind      string
	codec     string
	timescale uint32
	width     uint32
	height    uint32

	stts *gomp4.Stts
	stsc *gomp4.Stsc
	stsz *gomp4.Stsz
	stco *gomp4.Stco
	co64 *gomp4.Co64
	stss *gomp4.Stss
}

func (s *scanner) parseTKHD(box BoxRef, tb *trackBuilder) {
	buf := make([]byte, 92)
	if err := s.readAt(box.Offset+box.HeaderSize, buf); err != nil {
		return
	}
	if buf[0] == 1 {
		tb.id = binary.BigEndian.Uint32(buf[20:24])
	} else {
		tb.id = binary.BigEndian.Uint32(buf[12:16])
	}
}

func (s *scanner) parseHDLR(box BoxRef, tb *trackBuilder) {
	var buf [12]byte
	if err := s.readAt(box.Offset+box.HeaderSize, buf[:]); err != nil {
		return
	}
	switch fourCC(buf[8:12]) {
	case "vide":
		tb.kind = "video"
	case "soun":
		tb.kind = "audio"
	default:
		tb.kind = fourCC(buf[8:12])
	}
}

func (s *scanner) parseSTSD(box BoxRef, tb *trackBuilder) {
	var buf [40]byte
	start := box.Offset + box.HeaderSize
	if err := s.readAt(start, buf[:16]); err != nil {
		return
	}
	if binary.BigEndian.Uint32(buf[4:8]) == 0 {
		return
	}
	if err := s.readAt(start+8, buf[:40]); err != nil {
		return
	}
	tb.codec = fourCC(buf[4:8]) // entry header begins at payload+8
	if tb.kind == "video" {
		tb.width = uint32(binary.BigEndian.Uint16(buf[32:34]))
		tb.height = uint32(binary.BigEndian.Uint16(buf[34:36]))
	}
}

func samplesPerChunk(entries []gomp4.StscEntry, chunk uint32) uint32 {
	var spc uint32
	for _, e := range entries {
		if e.FirstChunk <= chunk {
			spc = e.SamplesPerChunk
		} else {
			break
		}
	}
	return spc
}

func (tb *trackBuilder) build(s *scanner) (*Track, error) {
	if tb.kind == "" {
		tb.kind = "unknown"
	}
	tr := &Track{ID: tb.id, Kind: tb.kind, Codec: tb.codec, Timescale: tb.timescale, Width: tb.width, Height: tb.height}
	if tb.stsz == nil || tb.stsc == nil || tb.stts == nil || (tb.stco == nil && tb.co64 == nil) {
		// Data/reference tracks may omit media tables. Keep them parseable but empty.
		return tr, nil
	}
	count := tb.stsz.SampleCount
	sizes := make([]int64, count)
	for i := uint32(0); i < count; i++ {
		if tb.stsz.SampleSize != 0 {
			sizes[i] = int64(tb.stsz.SampleSize)
		} else if int(i) < len(tb.stsz.EntrySize) {
			sizes[i] = int64(tb.stsz.EntrySize[i])
		} else {
			return nil, s.fail(fault.InvalidBox, -1, -1, "stsz entry %d missing", i+1)
		}
	}

	dts := make([]int64, count)
	durs := make([]uint32, count)
	var idx uint32
	var at int64
	for _, run := range tb.stts.Entries {
		for k := uint32(0); k < run.SampleCount && idx < count; k++ {
			dts[idx] = at
			durs[idx] = run.SampleDelta
			at += int64(run.SampleDelta)
			idx++
		}
	}
	if idx != count {
		return nil, s.fail(fault.InvalidBox, -1, at, "stts covers %d samples, want %d", idx, count)
	}

	var chunks []uint64
	if tb.stco != nil {
		for _, o := range tb.stco.ChunkOffset {
			chunks = append(chunks, uint64(o))
		}
	} else {
		chunks = tb.co64.ChunkOffset
	}
	sync := make(map[uint32]bool)
	if tb.stss != nil {
		for _, n := range tb.stss.SampleNumber {
			sync[n] = true
		}
	}
	tr.Samples = make([]Sample, count)
	var sample uint32
	for ci, off := range chunks {
		spc := samplesPerChunk(tb.stsc.Entries, uint32(ci+1))
		pos := int64(off)
		for j := uint32(0); j < spc && sample < count; j++ {
			size := sizes[sample]
			isSync := tb.stss == nil && tb.kind == "video"
			if sync[sample+1] {
				isSync = true
			}
			tr.Samples[sample] = Sample{Index: sample, Offset: pos, Size: size, DTS: dts[sample], Duration: durs[sample], Sync: isSync}
			end := uint64(pos) + uint64(size)
			if uint64(pos) > uint64(s.size) {
				return nil, s.fail(fault.SampleOffset, pos, dts[sample], "sample %d offset beyond file", sample+1)
			}
			if size < 0 || end > uint64(s.size) || end < uint64(pos) {
				first := s.size
				if uint64(pos) > uint64(s.size) {
					first = pos
				}
				return nil, s.fail(fault.SampleOffset, first, dts[sample], "sample %d range [%d,%d) beyond file size %d", sample+1, pos, pos+size, s.size)
			}
			pos += size
			sample++
		}
	}
	if sample != count {
		return nil, s.fail(fault.InvalidBox, -1, -1, "stsc/chunks address %d samples, stsz declares %d", sample, count)
	}
	return tr, nil
}

// ReadSampleAt reads one sample's bytes through r.
func (m *Movie) ReadSampleAt(r io.ReaderAt, kind string, index int) ([]byte, error) {
	tr := m.Track(kind)
	if tr == nil {
		return nil, fault.New(fault.Unsupported, fault.StageScan, m.Path, -1, -1, "missing %s track", kind)
	}
	if index < 0 || index >= len(tr.Samples) {
		return nil, fault.New(fault.SampleOffset, fault.StageScan, m.Path, -1, -1, "sample index %d out of range", index)
	}
	smp := tr.Samples[index]
	buf := make([]byte, smp.Size)
	n, err := io.ReadFull(io.NewSectionReader(r, smp.Offset, smp.Size), buf)
	if err != nil {
		off := smp.Offset + int64(n)
		return nil, fault.New(fault.SampleOffset, fault.StageScan, m.Path, off, smp.DTS, "read sample: %v", err)
	}
	return buf, nil
}
