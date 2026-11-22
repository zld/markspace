// Package fixture builds deterministic synthetic MP4/MOV inputs and network
// fault scenarios. Every byte is derived from the caller's seed; nothing is
// read from external files.
package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"
)

// Sample describes one sample before layout.
type Sample struct {
	Delta  uint32 // decode duration in track ticks
	Size   uint32 // payload bytes
	Sync   bool   // keyframe
	Ctts   int32  // composition offset (used only when useCtts)
	HasCtt bool
}

// TrackSpec is one track in a generated movie.
type TrackSpec struct {
	ID        uint32
	Handler   string // "vide" or "soun"
	Timescale uint32
	Width     uint16
	Height    uint16
	Samples   []Sample
	Seed      uint64
}

// MovieSpec is a complete standalone file.
type MovieSpec struct {
	Brand     string // "isom" or "qt  "
	Timescale uint32
	Tracks    []TrackSpec
}

// BuildMovie encodes spec as ftyp | mdat | moov. Each track uses one chunk
// per run of consecutive samples, exercising stsc run-length parsing.
func BuildMovie(spec MovieSpec) ([]byte, error) {
	if spec.Timescale == 0 {
		return nil, fmt.Errorf("fixture: movie timescale must be non-zero")
	}
	if spec.Brand == "" {
		spec.Brand = "isom"
	}
	if len(spec.Brand) != 4 {
		return nil, fmt.Errorf("fixture: brand %q must be 4 bytes", spec.Brand)
	}
	if len(spec.Tracks) == 0 {
		return nil, fmt.Errorf("fixture: at least one track required")
	}
	for i := range spec.Tracks {
		t := &spec.Tracks[i]
		if t.Timescale == 0 {
			return nil, fmt.Errorf("fixture: track %d timescale is zero", t.ID)
		}
		if t.Handler != "vide" && t.Handler != "soun" {
			return nil, fmt.Errorf("fixture: track %d handler %q unsupported", t.ID, t.Handler)
		}
		if len(t.Samples) == 0 {
			return nil, fmt.Errorf("fixture: track %d has no samples", t.ID)
		}
	}

	ftyp := makeFtyp(spec.Brand)
	mdatPayload, chunkOffsets, err := makeMediaData(spec)
	if err != nil {
		return nil, err
	}
	// mdat starts right after ftyp; its payload starts 8 bytes in.
	mdatHeader := 8
	payloadBase := uint64(len(ftyp) + mdatHeader)
	for ti := range chunkOffsets {
		for ci := range chunkOffsets[ti] {
			chunkOffsets[ti][ci] += payloadBase
		}
	}
	mdat := make([]byte, 0, mdatHeader+len(mdatPayload))
	mdat = appendBoxHeader(mdat, uint32(mdatHeader+len(mdatPayload)), "mdat")
	mdat = append(mdat, mdatPayload...)
	moov := makeMoov(spec, chunkOffsets)

	out := make([]byte, 0, len(ftyp)+len(mdat)+len(moov))
	out = append(out, ftyp...)
	out = append(out, mdat...)
	out = append(out, moov...)
	return out, nil
}

func makeFtyp(brand string) []byte {
	body := make([]byte, 0, 16)
	body = append(body, brand...)
	body = binary.BigEndian.AppendUint32(body, 0x200)
	body = append(body, "isom"...)
	if brand == "qt  " {
		body = append(body, "qt  "...)
	} else {
		body = append(body, "iso2"...)
	}
	out := make([]byte, 0, len(body)+8)
	out = appendBoxHeader(out, uint32(len(body)+8), "ftyp")
	return append(out, body...)
}

// makeMediaData writes sample payloads and returns chunk starts relative to the
// mdat payload (0-based) per track. Chunks are whole tracks concatenated; each
// run of 4 consecutive samples forms one chunk.
func makeMediaData(spec MovieSpec) ([]byte, [][]uint64, error) {
	var payload []byte
	chunkOffsets := make([][]uint64, len(spec.Tracks))
	for ti, tr := range spec.Tracks {
		rng := rand.New(rand.NewSource(int64(tr.Seed)))
		pos := uint64(len(payload))
		for si, s := range tr.Samples {
			if s.Size == 0 {
				return nil, nil, fmt.Errorf("fixture: track %d sample %d has zero size", tr.ID, si)
			}
			if si%4 == 0 {
				chunkOffsets[ti] = append(chunkOffsets[ti], pos)
			}
			buf := make([]byte, s.Size)
			if _, err := rng.Read(buf); err != nil {
				return nil, nil, err
			}
			payload = append(payload, buf...)
			pos += uint64(s.Size)
		}
	}
	return payload, chunkOffsets, nil
}

func makeMoov(spec MovieSpec, chunkOffsets [][]uint64) []byte {
	var traks []byte
	nextTrack := uint32(1)
	var movieDur uint64
	for ti, tr := range spec.Tracks {
		traks = append(traks, makeTrak(tr, chunkOffsets[ti])...)
		nextTrack++
		var ticks uint64
		for _, s := range tr.Samples {
			ticks += uint64(s.Delta)
		}
		scaled := ticks * uint64(spec.Timescale) / uint64(tr.Timescale)
		if scaled > movieDur {
			movieDur = scaled
		}
		_ = ti
	}
	var body []byte
	body = append(body, makeMvhd(spec.Timescale, movieDur, nextTrack)...)
	body = append(body, traks...)
	return wrapBox("moov", body)
}

func makeMvhd(timescale uint32, duration uint64, nextTrack uint32) []byte {
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, 0) // creation_time
	b = binary.BigEndian.AppendUint32(b, 0) // modification_time
	b = binary.BigEndian.AppendUint32(b, timescale)
	b = binary.BigEndian.AppendUint32(b, uint32(duration))
	b = binary.BigEndian.AppendUint32(b, 0x00010000) // rate
	b = binary.BigEndian.AppendUint16(b, 0x0100)     // volume
	b = append(b, 0, 0)                              // reserved
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 0)            // reserved[2]
	b = appendMatrix(b)
	b = append(b, make([]byte, 24)...) // pre_defined[6]
	b = binary.BigEndian.AppendUint32(b, nextTrack)
	return wrapBox("mvhd", b)
}

func makeTrak(tr TrackSpec, chunks []uint64) []byte {
	var body []byte
	body = append(body, makeTkhd(tr)...)
	body = append(body, makeMdia(tr, chunks)...)
	return wrapBox("trak", body)
}

func makeTkhd(tr TrackSpec) []byte {
	var b []byte
	b = appendFullHeader(b, 0, 7) // enabled | in_movie | in_preview
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, tr.ID)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, 0) // duration v0
	b = append(b, make([]byte, 8)...)       // reserved[2]
	b = binary.BigEndian.AppendUint16(b, 0) // layer
	b = binary.BigEndian.AppendUint16(b, 0) // alternate_group
	if tr.Handler == "soun" {
		b = binary.BigEndian.AppendUint16(b, 0x0100)
	} else {
		b = binary.BigEndian.AppendUint16(b, 0)
	}
	b = binary.BigEndian.AppendUint16(b, 0)
	b = appendMatrix(b)
	b = binary.BigEndian.AppendUint32(b, uint32(tr.Width)<<16)
	b = binary.BigEndian.AppendUint32(b, uint32(tr.Height)<<16)
	return wrapBox("tkhd", b)
}

func makeMdia(tr TrackSpec, chunks []uint64) []byte {
	var body []byte
	body = append(body, makeMdhd(tr.Timescale, totalTicks(tr))...)
	body = append(body, makeHdlr(tr.Handler)...)
	body = append(body, makeMinf(tr, chunks)...)
	return wrapBox("mdia", body)
}

func makeMdhd(timescale uint32, duration uint64) []byte {
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, timescale)
	b = binary.BigEndian.AppendUint32(b, uint32(duration))
	b = binary.BigEndian.AppendUint16(b, 0x55c4) // "und"
	b = binary.BigEndian.AppendUint16(b, 0)
	return wrapBox("mdhd", b)
}

func makeHdlr(handler string) []byte {
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, handler...)
	b = append(b, make([]byte, 12)...)
	b = append(b, 0) // empty C string name
	return wrapBox("hdlr", b)
}

func makeMinf(tr TrackSpec, chunks []uint64) []byte {
	var b []byte
	if tr.Handler == "soun" {
		smhd := appendFullHeader(nil, 0, 0)
		smhd = binary.BigEndian.AppendUint16(smhd, 0) // balance
		smhd = binary.BigEndian.AppendUint16(smhd, 0) // reserved
		b = append(b, wrapBox("smhd", smhd)...)
	} else {
		vmhd := appendFullHeader(nil, 0, 1)
		vmhd = binary.BigEndian.AppendUint16(vmhd, 0)
		vmhd = append(vmhd, make([]byte, 6)...)
		b = append(b, wrapBox("vmhd", vmhd)...)
	}
	dref := appendFullHeader(nil, 0, 0)
	dref = binary.BigEndian.AppendUint32(dref, 1)
	url := appendFullHeader(nil, 0, 1) // self-contained
	dref = append(dref, wrapBox("url ", url)...)
	dinf := wrapBox("dref", dref)
	b = append(b, wrapBox("dinf", dinf)...)
	b = append(b, makeStbl(tr, chunks)...)
	return wrapBox("minf", b)
}

func makeStbl(tr TrackSpec, chunks []uint64) []byte {
	var b []byte
	b = append(b, makeStsd(tr)...)
	b = append(b, makeStts(tr.Samples)...)
	if hasCtts(tr.Samples) {
		b = append(b, makeCtts(tr.Samples)...)
	}
	if tr.Handler == "vide" {
		if stss := makeStss(tr.Samples); stss != nil {
			b = append(b, stss...)
		}
	}
	b = append(b, makeStsc(tr.Samples)...)
	b = append(b, makeStsz(tr.Samples)...)
	b = append(b, makeStco(chunks)...)
	return wrapBox("stbl", b)
}

func makeStsd(tr TrackSpec) []byte {
	var entry []byte
	if tr.Handler == "soun" {
		entry = makeAudioSampleEntry()
	} else {
		entry = makeVideoSampleEntry(tr.Width, tr.Height)
	}
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = append(b, entry...)
	return wrapBox("stsd", b)
}

func makeVideoSampleEntry(w, h uint16) []byte {
	var b []byte
	b = append(b, make([]byte, 6)...)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint16(b, 0)
	b = binary.BigEndian.AppendUint16(b, 0)
	b = append(b, make([]byte, 12)...)
	b = binary.BigEndian.AppendUint16(b, w)
	b = binary.BigEndian.AppendUint16(b, h)
	b = binary.BigEndian.AppendUint32(b, 0x00480000)
	b = binary.BigEndian.AppendUint32(b, 0x00480000)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, make([]byte, 32)...)
	b = binary.BigEndian.AppendUint16(b, 0x0018)
	b = binary.BigEndian.AppendUint16(b, 0xffff)
	// AVC configuration box with a short but well-formed record.
	avcc := []byte{1, 0x64, 0x00, 0x1f, 0xff, 0xe1, 0x00, 0x04, 0x67, 0x64, 0x00, 0x1f, 1, 0x00, 0x04, 0x68, 0xee, 0x3c, 0x80}
	b = append(b, wrapBox("avcC", avcc)...)
	return wrapBox("avc1", b)
}

func makeAudioSampleEntry() []byte {
	var b []byte
	b = append(b, make([]byte, 6)...)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint16(b, 2) // channels
	b = binary.BigEndian.AppendUint16(b, 16)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, 48000<<16)
	return wrapBox("mp4a", b)
}

func makeStts(samples []Sample) []byte {
	type run struct {
		count uint32
		delta uint32
	}
	var runs []run
	for _, s := range samples {
		if n := len(runs); n > 0 && runs[n-1].delta == s.Delta {
			runs[n-1].count++
		} else {
			runs = append(runs, run{1, s.Delta})
		}
	}
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(runs)))
	for _, r := range runs {
		b = binary.BigEndian.AppendUint32(b, r.count)
		b = binary.BigEndian.AppendUint32(b, r.delta)
	}
	return wrapBox("stts", b)
}

func hasCtts(samples []Sample) bool {
	for _, s := range samples {
		if s.HasCtt {
			return true
		}
	}
	return false
}

func makeCtts(samples []Sample) []byte {
	type run struct {
		count uint32
		off   int32
	}
	var runs []run
	for _, s := range samples {
		off := s.Ctts
		if n := len(runs); n > 0 && runs[n-1].off == off {
			runs[n-1].count++
		} else {
			runs = append(runs, run{1, off})
		}
	}
	var b []byte
	b = appendFullHeader(b, 1, 0) // version 1: signed offsets
	b = binary.BigEndian.AppendUint32(b, uint32(len(runs)))
	for _, r := range runs {
		b = binary.BigEndian.AppendUint32(b, r.count)
		b = binary.BigEndian.AppendUint32(b, uint32(r.off))
	}
	return wrapBox("ctts", b)
}

func makeStss(samples []Sample) []byte {
	var nums []uint32
	for i, s := range samples {
		if s.Sync {
			nums = append(nums, uint32(i+1))
		}
	}
	if len(nums) == 0 {
		return nil
	}
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(nums)))
	for _, n := range nums {
		b = binary.BigEndian.AppendUint32(b, n)
	}
	return wrapBox("stss", b)
}

func makeStsc(samples []Sample) []byte {
	const perChunk = 4
	type entry struct{ first, count uint32 }
	var runs []entry
	chunks := len(samples) / perChunk
	if chunks > 0 {
		runs = append(runs, entry{1, perChunk})
	}
	if rem := len(samples) % perChunk; rem > 0 {
		runs = append(runs, entry{uint32(chunks + 1), uint32(rem)})
	}
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(runs)))
	for _, r := range runs {
		b = binary.BigEndian.AppendUint32(b, r.first)
		b = binary.BigEndian.AppendUint32(b, r.count)
		b = binary.BigEndian.AppendUint32(b, 1)
	}
	return wrapBox("stsc", b)
}

func makeStsz(samples []Sample) []byte {
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(samples)))
	for _, s := range samples {
		b = binary.BigEndian.AppendUint32(b, s.Size)
	}
	return wrapBox("stsz", b)
}

func makeStco(chunks []uint64) []byte {
	var b []byte
	b = appendFullHeader(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(chunks)))
	for _, off := range chunks {
		if off > 0xffffffff {
			panic("fixture: chunk offset exceeds 32 bits; use co64")
		}
		b = binary.BigEndian.AppendUint32(b, uint32(off))
	}
	return wrapBox("stco", b)
}

func totalTicks(tr TrackSpec) uint64 {
	var t uint64
	for _, s := range tr.Samples {
		t += uint64(s.Delta)
	}
	return t
}

func appendMatrix(b []byte) []byte {
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		b = binary.BigEndian.AppendUint32(b, v)
	}
	return b
}

func appendFullHeader(b []byte, version uint8, flags uint32) []byte {
	b = append(b, version, byte(flags>>16), byte(flags>>8), byte(flags))
	return b
}

func appendBoxHeader(b []byte, size uint32, typ string) []byte {
	b = binary.BigEndian.AppendUint32(b, size)
	b = append(b, typ...)
	return b
}

func wrapBox(typ string, body []byte) []byte {
	out := make([]byte, 0, 8+len(body))
	out = appendBoxHeader(out, uint32(8+len(body)), typ)
	return append(out, body...)
}
