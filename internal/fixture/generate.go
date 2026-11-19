// Package fixture generates deterministic MP4 fixtures covering edit
// lists, 32-bit decode-time wraparound, multiple video tracks, a track
// with no keyframes, a truncated box and a fragmented file.
package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// box assembles size+type+payload.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

// fullbox prepends version/flags to the payload.
func fullbox(typ string, version byte, flags uint32, payload []byte) []byte {
	fb := make([]byte, 4+len(payload))
	fb[0] = version
	fb[1] = byte(flags >> 16)
	fb[2] = byte(flags >> 8)
	fb[3] = byte(flags)
	copy(fb[4:], payload)
	return box(typ, fb)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func concat(parts ...[]byte) []byte {
	var n int
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// samplePayload is deterministic: track and sample number plus a pattern.
func samplePayload(track, sample int) []byte {
	size := 32 + (sample%3)*8
	b := make([]byte, size)
	binary.BigEndian.PutUint64(b[0:8], uint64(track))
	binary.BigEndian.PutUint64(b[8:16], uint64(sample))
	for j := 16; j < size; j++ {
		b[j] = byte((track*7 + sample*31 + j) % 251)
	}
	return b
}

func ftyp() []byte {
	return box("ftyp", concat([]byte("isom"), u32(0x200), []byte("isomiso2avc1mp41")))
}

func mvhd(timescale, duration uint32, nextTrack uint32) []byte {
	p := concat(u32(0), u32(0), u32(timescale), u32(duration),
		u32(0x00010000), []byte{0x01, 0x00, 0, 0}, make([]byte, 8),
		// unity matrix
		u32(0x00010000), u32(0), u32(0), u32(0), u32(0x00010000), u32(0), u32(0), u32(0), u32(0x40000000),
		make([]byte, 24), u32(nextTrack))
	return fullbox("mvhd", 0, 0, p)
}

func tkhd(trackID, duration uint32) []byte {
	p := concat(u32(0), u32(0), u32(trackID), u32(0), u32(duration),
		make([]byte, 8), []byte{0, 0, 0, 0, 0, 0, 0, 0},
		u32(0x00010000), u32(0), u32(0), u32(0), u32(0x00010000), u32(0), u32(0), u32(0), u32(0x40000000),
		u32(320<<16), u32(240<<16))
	return fullbox("tkhd", 0, 0x3, p)
}

func mdhdV1(timescale uint32, duration uint64) []byte {
	return fullbox("mdhd", 1, 0, concat(u64(0), u64(0), u32(timescale), u64(duration), []byte{0x55, 0xc4, 0, 0}))
}

func mdhdV0(timescale, duration uint32) []byte {
	return fullbox("mdhd", 0, 0, concat(u32(0), u32(0), u32(timescale), u32(duration), []byte{0x55, 0xc4, 0, 0}))
}

func hdlr(handler string) []byte {
	return fullbox("hdlr", 0, 0, concat(u32(0), []byte(handler), make([]byte, 12), []byte("mp4seek\x00")))
}

func stsd() []byte {
	entry := concat(make([]byte, 6), []byte{0, 1}, make([]byte, 16),
		[]byte{0x01, 0x40, 0x00, 0xf0}, u32(0x00480000), u32(0x00480000), u32(0),
		[]byte{1}, make([]byte, 31), []byte{0, 0x18, 0xff, 0xff})
	avc1 := box("avc1", concat(entry, box("avcC", []byte{1, 0x64, 0, 0x1f, 0xff, 0, 0})))
	return fullbox("stsd", 0, 0, concat(u32(1), avc1))
}

func minf(stbl []byte) []byte {
	vmhd := fullbox("vmhd", 0, 1, make([]byte, 8))
	dref := fullbox("dref", 0, 0, concat(u32(1), fullbox("url ", 0, 1, nil)))
	return box("minf", concat(vmhd, box("dinf", dref), stbl))
}

// trackSpec describes one fixture track.
type trackSpec struct {
	id        int
	timescale uint32
	mdhdV1    bool
	duration  uint64 // media duration in timescale units
	tkhdDur   uint32
	stts      [][2]uint32 // count, delta
	ctts      [][2]int32  // count, signed offset (version 1); nil = none
	stss      []uint32    // nil = no stss box (all sync); empty = no keyframes
	hasStss   bool
	samples   [][]byte
	edits     []editSpec
	co64      bool
}

type editSpec struct {
	segDur    uint32
	mediaTime int32
}

func buildTrak(ts *trackSpec, chunkOffset uint64) []byte {
	stts := make([]byte, 0, 8*len(ts.stts))
	var count uint32
	for _, e := range ts.stts {
		stts = append(stts, concat(u32(e[0]), u32(e[1]))...)
		count += e[0]
	}
	sttsBox := fullbox("stts", 0, 0, concat(u32(uint32(len(ts.stts))), stts))

	var cttsBox []byte
	if ts.ctts != nil {
		c := concat(u32(uint32(len(ts.ctts))))
		for _, e := range ts.ctts {
			c = append(c, concat(u32(uint32(e[0])), u32(uint32(e[1])))...)
		}
		cttsBox = fullbox("ctts", 1, 0, c)
	}

	var stssBox []byte
	if ts.hasStss {
		c := concat(u32(uint32(len(ts.stss))))
		for _, s := range ts.stss {
			c = append(c, u32(s)...)
		}
		stssBox = fullbox("stss", 0, 0, c)
	}

	stsc := fullbox("stsc", 0, 0, concat(u32(1), u32(1), u32(count), u32(1)))

	sz := concat(u32(0), u32(count))
	for _, s := range ts.samples {
		sz = append(sz, u32(uint32(len(s)))...)
	}
	stsz := fullbox("stsz", 0, 0, sz)

	var coBox []byte
	if ts.co64 {
		coBox = fullbox("co64", 0, 0, concat(u32(1), u64(chunkOffset)))
	} else {
		coBox = fullbox("stco", 0, 0, concat(u32(1), u32(uint32(chunkOffset))))
	}

	stbl := box("stbl", concat(stsd(), sttsBox, cttsBox, stssBox, stsc, stsz, coBox))

	var mdhd []byte
	if ts.mdhdV1 {
		mdhd = mdhdV1(ts.timescale, ts.duration)
	} else {
		mdhd = mdhdV0(ts.timescale, uint32(ts.duration))
	}
	mdia := box("mdia", concat(mdhd, hdlr("vide"), minf(stbl)))

	var edts []byte
	if ts.edits != nil {
		e := concat(u32(uint32(len(ts.edits))))
		for _, ed := range ts.edits {
			e = append(e, concat(u32(ed.segDur), u32(uint32(ed.mediaTime)), []byte{0, 1, 0, 0})...)
		}
		edts = box("edts", fullbox("elst", 0, 0, e))
	}
	return box("trak", concat(tkhd(uint32(ts.id), ts.tkhdDur), edts, mdia))
}

// buildFile assembles ftyp+moov+mdat, patching chunk offsets.
func buildFile(movieTS, movieDur uint32, tracks []*trackSpec) []byte {
	// First pass: moov with zero offsets to learn its length.
	moov0 := box("moov", concat(mvhd(movieTS, movieDur, uint32(len(tracks)+1)), buildAllTraks(tracks, nil)))
	mdatHead := 8
	offsets := make([]uint64, len(tracks))
	pos := uint64(len(ftyp()) + len(moov0) + mdatHead)
	for i, ts := range tracks {
		offsets[i] = pos
		for _, s := range ts.samples {
			pos += uint64(len(s))
		}
	}
	moov := box("moov", concat(mvhd(movieTS, movieDur, uint32(len(tracks)+1)), buildAllTraks(tracks, offsets)))
	var mdatPayload []byte
	for _, ts := range tracks {
		for _, s := range ts.samples {
			mdatPayload = append(mdatPayload, s...)
		}
	}
	return concat(ftyp(), moov, box("mdat", mdatPayload))
}

func buildAllTraks(tracks []*trackSpec, offsets []uint64) []byte {
	var out []byte
	for i, ts := range tracks {
		var off uint64
		if offsets != nil {
			off = offsets[i]
		}
		out = append(out, buildTrak(ts, off)...)
	}
	return out
}

func samplesOf(track, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = samplePayload(track, i)
	}
	return out
}

// Generate writes all fixtures into dir and returns the file paths.
func Generate(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var files []string
	write := func(name string, data []byte) error {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		files = append(files, p)
		return nil
	}

	// edit-wrap.mp4: two video tracks. Track 1: 8 samples, 2 edits,
	// one 32-bit decode-time wraparound. Track 2: 4 samples, stss
	// present but empty (no keyframes).
	t1 := &trackSpec{
		id: 1, timescale: 90000, mdhdV1: true,
		duration: 4295192296, tkhdDur: 20000,
		stts:    [][2]uint32{{1, 4294562296}, {7, 90000}},
		ctts:    [][2]int32{{1, 18000}, {1, 0}, {1, 9000}, {5, 0}},
		hasStss: true, stss: []uint32{1, 5},
		samples: samplesOf(1, 8),
		edits:   []editSpec{{5000, -1}, {15000, 0}},
	}
	t2 := &trackSpec{
		id: 2, timescale: 90000,
		duration: 360000, tkhdDur: 4000,
		stts:    [][2]uint32{{4, 90000}},
		hasStss: true, stss: []uint32{},
		samples: samplesOf(2, 4),
		co64:    true,
	}
	if err := write("edit-wrap.mp4", buildFile(1000, 20000, []*trackSpec{t1, t2})); err != nil {
		return nil, err
	}

	// truncated.mp4: moov declares a size far beyond EOF.
	trunc := concat(ftyp(), u32(1<<20), []byte("moov"), make([]byte, 16))
	if err := write("truncated.mp4", trunc); err != nil {
		return nil, err
	}

	// frag.mp4: mvex + moof mark a fragmented MP4 (unsupported).
	frag := concat(ftyp(),
		box("moov", concat(mvhd(1000, 0, 2), box("mvex", fullbox("trex", 0, 0, concat(u32(1), u32(1), u32(0), u32(0), u32(0)))))),
		box("moof", fullbox("mfhd", 0, 0, u32(1))))
	if err := write("frag.mp4", frag); err != nil {
		return nil, err
	}

	if len(files) != 3 {
		return nil, fmt.Errorf("fixture: expected 3 files, wrote %d", len(files))
	}
	return files, nil
}
