// Package media reads MP4 container tracks and sample tables using
// go-mp4, and writes synthetic MP4 fixtures. Truncated samples or invalid
// chunk indexes stop the affected track at the corruption offset while
// leaving independent tracks readable.
package media

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediapack/merr"
)

// Sample is one demuxed access unit.
type Sample struct {
	Index  int
	DTS    int64 // decoding time in track timescale ticks
	Size   uint32
	Offset uint64 // absolute file offset of the payload
	Data   []byte
}

// Track is a demuxed MP4 track with its sample table.
type Track struct {
	TrackID       uint32
	MediaType     string // "vide" / "soun"
	Timescale     uint32
	Samples       []Sample
	CorruptOffset int64 // byte offset where corruption was found, -1 if clean
	CorruptReason string
}

// File is a demuxed MP4 container.
type File struct {
	Tracks []*Track
}

// ---- synthetic writer -------------------------------------------------

// SynthTrack describes a track to synthesize.
type SynthTrack struct {
	TrackID   uint32
	MediaType string
	Timescale uint32
	Delta     uint32 // ticks per sample
	DTSBase   uint32 // non-zero start offset, in ticks
	Sizes     []uint32
	Fill      func(track uint32, index int, size int) []byte
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

func fullbox(typ string, version byte, flags uint32, payload []byte) []byte {
	p := make([]byte, 4, 4+len(payload))
	p[0] = version
	p[1], p[2], p[3] = byte(flags>>16), byte(flags>>8), byte(flags)
	return box(typ, append(p, payload...))
}

var unityMatrix = []byte{
	0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0,
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func mvhd(timescale, duration uint32, nextTrackID uint32) []byte {
	p := cat(be32(0), be32(0), be32(timescale), be32(duration),
		be32(0x00010000), be16(0x0100), be16(0), make([]byte, 8),
		unityMatrix, make([]byte, 24), be32(nextTrackID))
	return fullbox("mvhd", 0, 0, p)
}

func tkhd(t SynthTrack, duration uint32) []byte {
	var w, h uint32
	if t.MediaType == "vide" {
		w, h = 1280<<16, 720<<16
	}
	p := cat(be32(0), be32(0), be32(t.TrackID), be32(0), be32(duration),
		make([]byte, 8), be16(0), be16(0), be16(0), be16(0),
		unityMatrix, be32(w), be32(h))
	return fullbox("tkhd", 0, 7, p)
}

func mdhd(t SynthTrack, duration uint32) []byte {
	p := cat(be32(0), be32(0), be32(t.Timescale), be32(duration), be16(0x55c4), be16(0))
	return fullbox("mdhd", 0, 0, p)
}

func hdlr(mediaType string) []byte {
	name := "MediaHandler"
	p := cat(be32(0), []byte(mediaType), make([]byte, 12), []byte(name), []byte{0})
	return fullbox("hdlr", 0, 0, p)
}

func stbl(t SynthTrack, chunkOffsets []uint32) []byte {
	stsd := fullbox("stsd", 0, 0, cat(be32(1), box("raw ", cat(make([]byte, 6), be16(1)))))
	stts := fullbox("stts", 0, 0, cat(be32(1), be32(uint32(len(t.Sizes))), be32(t.Delta)))
	stsc := fullbox("stsc", 0, 0, cat(be32(1), be32(1), be32(1), be32(1)))
	sz := cat(be32(0), be32(uint32(len(t.Sizes))))
	for _, s := range t.Sizes {
		sz = append(sz, be32(s)...)
	}
	stsz := fullbox("stsz", 0, 0, sz)
	co := cat(be32(uint32(len(chunkOffsets))))
	for _, o := range chunkOffsets {
		co = append(co, be32(o)...)
	}
	stco := fullbox("stco", 0, 0, co)
	return box("stbl", cat(stsd, stts, stsc, stsz, stco))
}

func moovBox(tracks []SynthTrack, offsets [][]uint32) []byte {
	var maxDur uint32
	for i, t := range tracks {
		d := t.DTSBase + t.Delta*uint32(len(t.Sizes))
		ms := uint32(uint64(d) * 1000 / uint64(t.Timescale))
		if ms > maxDur {
			maxDur = ms
		}
		_ = i
	}
	out := mvhd(1000, maxDur, uint32(len(tracks))+1)
	for i, t := range tracks {
		dur := t.DTSBase + t.Delta*uint32(len(t.Sizes))
		var mediaHeader []byte
		if t.MediaType == "vide" {
			mediaHeader = fullbox("vmhd", 0, 1, cat(be16(0), be16(0), be16(0), be16(0)))
		} else {
			mediaHeader = fullbox("smhd", 0, 0, cat(be16(0), be16(0)))
		}
		dinf := box("dinf", fullbox("dref", 0, 0, cat(be32(1), fullbox("url ", 0, 1, nil))))
		minf := box("minf", cat(mediaHeader, dinf, stbl(t, offsets[i])))
		mdia := box("mdia", cat(mdhd(t, dur), hdlr(t.MediaType), minf))
		out = append(out, box("trak", cat(tkhd(t, uint32(uint64(dur)*1000/uint64(t.Timescale))), mdia))...)
	}
	return box("moov", out)
}

// WriteSyntheticMP4 writes a minimal but well-formed MP4 with one sample
// per chunk, so chunk offsets equal sample offsets.
func WriteSyntheticMP4(path string, tracks []SynthTrack) error {
	ftyp := box("ftyp", cat([]byte("isom"), be32(0), []byte("isom"), []byte("iso2")))
	zero := make([][]uint32, len(tracks))
	for i, t := range tracks {
		zero[i] = make([]uint32, len(t.Sizes))
	}
	moovLen := len(moovBox(tracks, zero))
	// one sample per chunk: offset_i = mdatPayloadStart + cumsum(sizes)
	base := uint64(len(ftyp)) + uint64(moovLen) + 8
	offsets := make([][]uint32, len(tracks))
	var payloads [][]byte
	cur := base
	for i, t := range tracks {
		offsets[i] = make([]uint32, len(t.Sizes))
		for j, sz := range t.Sizes {
			offsets[i][j] = uint32(cur)
			cur += uint64(sz)
			data := make([]byte, sz)
			if t.Fill != nil {
				data = t.Fill(t.TrackID, j, int(sz))
			}
			payloads = append(payloads, data)
		}
	}
	moov := moovBox(tracks, offsets)
	mdatPayload := cat(payloads...)
	mdat := box("mdat", mdatPayload)
	return os.WriteFile(path, cat(ftyp, moov, mdat), 0o644)
}

// ---- reader (go-mp4) --------------------------------------------------

type rawTrack struct {
	id        uint32
	mediaType string
	timescale uint32
	stts      []mp4.SttsEntry
	stsc      []mp4.StscEntry
	sizes     []uint32
	chunks    []uint64
}

// ReadFile demuxes an MP4: track metadata, sample tables and payloads.
// A truncated sample or invalid chunk index stops that track at the
// corruption offset; other tracks are unaffected.
func ReadFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, merr.Wrap(merr.KindCorrupt, "demux", "", "open mp4", err)
	}
	defer f.Close()
	var raws []*rawTrack
	var cur *rawTrack
	var mdatOff, mdatSize uint64
	var haveMdat bool
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl():
			return h.Expand()
		case mp4.BoxTypeTrak():
			cur = &rawTrack{}
			raws = append(raws, cur)
			return h.Expand()
		case mp4.BoxTypeMdat():
			mdatOff = h.BoxInfo.Offset + uint64(h.BoxInfo.HeaderSize)
			mdatSize = h.BoxInfo.Size - uint64(h.BoxInfo.HeaderSize)
			haveMdat = true
			return nil, nil
		case mp4.BoxTypeTkhd(), mp4.BoxTypeMdhd(), mp4.BoxTypeHdlr(),
			mp4.BoxTypeStts(), mp4.BoxTypeStsc(), mp4.BoxTypeStsz(),
			mp4.BoxTypeStco(), mp4.BoxTypeCo64():
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if cur == nil {
				return nil, nil
			}
			switch b := b.(type) {
			case *mp4.Tkhd:
				cur.id = b.TrackID
			case *mp4.Mdhd:
				cur.timescale = b.Timescale
			case *mp4.Hdlr:
				cur.mediaType = string(b.HandlerType[:])
			case *mp4.Stts:
				cur.stts = b.Entries
			case *mp4.Stsc:
				cur.stsc = b.Entries
			case *mp4.Stsz:
				cur.sizes = b.EntrySize
				if b.SampleSize != 0 && len(cur.sizes) == 0 {
					for i := uint32(0); i < b.SampleCount; i++ {
						cur.sizes = append(cur.sizes, b.SampleSize)
					}
				}
			case *mp4.Stco:
				for _, o := range b.ChunkOffset {
					cur.chunks = append(cur.chunks, uint64(o))
				}
			case *mp4.Co64:
				cur.chunks = append(cur.chunks, b.ChunkOffset...)
			}
			return nil, nil
		}
		return nil, nil
	})
	if err != nil {
		return nil, merr.Wrap(merr.KindCorrupt, "demux", "", "parse mp4 box structure", err)
	}
	if !haveMdat {
		return nil, merr.Corruptf("demux", "", "no mdat box found")
	}
	out := &File{}
	for _, rt := range raws {
		out.Tracks = append(out.Tracks, buildTrack(f, rt, mdatOff, mdatSize))
	}
	return out, nil
}

// buildTrack expands the sample table and validates every sample against
// the mdat payload bounds, recording the corruption offset on failure.
func buildTrack(r io.ReaderAt, rt *rawTrack, mdatOff, mdatSize uint64) *Track {
	tr := &Track{TrackID: rt.id, MediaType: rt.mediaType, Timescale: rt.timescale, CorruptOffset: -1}
	fail := func(off int64, reason string) *Track {
		tr.CorruptOffset = off
		tr.CorruptReason = reason
		return tr
	}
	// expand decoding times from stts
	var dts []int64
	var cur int64
	for _, e := range rt.stts {
		for i := uint32(0); i < e.SampleCount; i++ {
			dts = append(dts, cur)
			cur += int64(e.SampleDelta)
		}
	}
	if len(dts) < len(rt.sizes) {
		return fail(int64(mdatOff), fmt.Sprintf("stts covers %d samples but stsz lists %d", len(dts), len(rt.sizes)))
	}
	// walk chunks via stsc; our writer emits one sample per chunk but the
	// reader handles the general case
	sampleIdx := 0
	for ci, chunkOff := range rt.chunks {
		// find applicable stsc entry (1-based FirstChunk)
		var perChunk uint32
		for _, e := range rt.stsc {
			if uint64(e.FirstChunk) <= uint64(ci)+1 {
				perChunk = e.SamplesPerChunk
			}
		}
		if perChunk == 0 {
			return fail(int64(chunkOff), fmt.Sprintf("no stsc entry for chunk %d", ci))
		}
		off := chunkOff
		for s := uint32(0); s < perChunk; s++ {
			if sampleIdx >= len(rt.sizes) {
				return fail(int64(off), fmt.Sprintf("chunk %d references sample %d beyond stsz (%d)", ci, sampleIdx, len(rt.sizes)))
			}
			sz := rt.sizes[sampleIdx]
			if off < mdatOff || off+uint64(sz) > mdatOff+mdatSize {
				return fail(int64(off), fmt.Sprintf("sample %d truncated: [%d,%d) outside mdat [%d,%d)",
					sampleIdx, off, off+uint64(sz), mdatOff, mdatOff+mdatSize))
			}
			data := make([]byte, sz)
			if _, err := r.ReadAt(data, int64(off)); err != nil {
				return fail(int64(off), fmt.Sprintf("sample %d read: %v", sampleIdx, err))
			}
			tr.Samples = append(tr.Samples, Sample{Index: sampleIdx, DTS: dts[sampleIdx], Size: sz, Offset: off, Data: data})
			off += uint64(sz)
			sampleIdx++
		}
	}
	if sampleIdx < len(rt.sizes) {
		return fail(int64(mdatOff), fmt.Sprintf("only %d of %d samples mapped to chunks", sampleIdx, len(rt.sizes)))
	}
	return tr
}
