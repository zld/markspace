// Package fixgen generates self-contained CMAF/fMP4 fixtures in pure Go.
// It can emit well-formed files as well as the four anomaly classes the
// pipeline must survive: mfhd sequence gaps, tfdt rollback, truncated
// boxes, and channel/samplerate/colour metadata conflicts.
package fixgen

import "encoding/binary"

// box serializes one ISO BMFF box: 4-byte size + 4-byte type + payload.
func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// fullBox serializes a FullBox (version + 24-bit flags).
func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(head, payload...))
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u24(v uint32) []byte { return []byte{byte(v >> 16), byte(v >> 8), byte(v)} }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// ColorSpec describes an nclx colour box for a video track.
type ColorSpec struct {
	Primaries  uint16
	Transfer   uint16
	Matrix     uint16
	FullRange  bool
}

// TrackSpec describes one track in the init segment.
type TrackSpec struct {
	ID         uint32
	Handler    string // "vide" or "soun"
	Timescale  uint32
	Channels   uint16 // audio
	SampleRate uint32 // audio, Hz
	Width      uint32 // video
	Height     uint32 // video
	Color      *ColorSpec
}

func ftyp() []byte {
	return box("ftyp", concat(
		[]byte("iso6"), u32(1),
		[]byte("iso6"), []byte("mp41"), []byte("cmfc"),
	))
}

func mvhd() []byte {
	return fullBox("mvhd", 0, 0, concat(
		u32(0), u32(0), // creation/modification time
		u32(1000),      // movie timescale
		u32(0),         // duration
		u32(0x00010000), u16(0x0100), u16(0), // rate, volume, reserved
		make([]byte, 10),
		// unity matrix
		u32(0x00010000), u32(0), u32(0), u32(0), u32(0x00010000), u32(0), u32(0), u32(0), u32(0x40000000),
		make([]byte, 24), // pre-defined
		u32(99), // next track id
	))
}

func tkhd(t TrackSpec) []byte {
	return fullBox("tkhd", 0, 0x000007, concat(
		u32(0), u32(0), u32(t.ID), u32(0), u32(0), // times, id, reserved, duration
		make([]byte, 8),
		u16(0), u16(0), u16(0), u16(0), // layer, alt group, volume, reserved
		u32(0x00010000), u32(0), u32(0), u32(0), u32(0x00010000), u32(0), u32(0), u32(0), u32(0x40000000),
		u32(t.Width<<16), u32(t.Height<<16),
	))
}

func mdhd(t TrackSpec) []byte {
	return fullBox("mdhd", 0, 0, concat(
		u32(0), u32(0), u32(t.Timescale), u32(0),
		u16(0x55c4), u16(0), // und language, pre-defined
	))
}

func hdlr(t TrackSpec) []byte {
	name := "VideoHandler"
	if t.Handler == "soun" {
		name = "SoundHandler"
	}
	return fullBox("hdlr", 0, 0, concat(
		u32(0), []byte(t.Handler), make([]byte, 12), append([]byte(name), 0),
	))
}

func colr(c ColorSpec) []byte {
	fr := byte(0)
	if c.FullRange {
		fr = 0x80
	}
	return box("colr", concat([]byte("nclx"), u16(c.Primaries), u16(c.Transfer), u16(c.Matrix), []byte{fr}))
}

func mp4a(t TrackSpec) []byte {
	return box("mp4a", concat(
		make([]byte, 6), u16(1), // reserved, data reference index
		make([]byte, 8),         // reserved
		u16(t.Channels), u16(16), // channelcount, samplesize
		u16(0), u16(0),           // pre-defined, reserved
		u32(t.SampleRate<<16),    // samplerate 16.16 fixed
	))
}

func avc1(t TrackSpec) []byte {
	entry := concat(
		make([]byte, 6), u16(1), // reserved, data reference index
		make([]byte, 16),        // pre-defined/reserved
		u16(uint16(t.Width)), u16(uint16(t.Height)),
		u32(0x00480000), u32(0x00480000), // horiz/vert resolution 72dpi
		u32(0), u16(1),                   // reserved, frame count
		make([]byte, 32),                 // compressor name
		u16(0x0018), u16(0xffff),         // depth, pre-defined
	)
	if t.Color != nil {
		entry = append(entry, colr(*t.Color)...)
	}
	return box("avc1", entry)
}

func stsd(t TrackSpec) []byte {
	var entry []byte
	if t.Handler == "soun" {
		entry = mp4a(t)
	} else {
		entry = avc1(t)
	}
	return fullBox("stsd", 0, 0, concat(u32(1), entry))
}

func stbl(t TrackSpec) []byte {
	empty := fullBox("stts", 0, 0, u32(0))
	return box("stbl", concat(
		stsd(t), empty,
		fullBox("stsc", 0, 0, u32(0)),
		fullBox("stsz", 0, 0, concat(u32(0), u32(0))),
		fullBox("stco", 0, 0, u32(0)),
	))
}

func minf(t TrackSpec) []byte {
	var mediaHeader []byte
	if t.Handler == "soun" {
		mediaHeader = fullBox("smhd", 0, 0, concat(u16(0), u16(0)))
	} else {
		mediaHeader = fullBox("vmhd", 0, 0x1, concat(u16(0), u16(0), u16(0), u16(0)))
	}
	dref := fullBox("dref", 0, 0, concat(u32(1), fullBox("url ", 0, 0x1, nil)))
	return box("minf", concat(mediaHeader, box("dinf", dref), stbl(t)))
}

func trak(t TrackSpec) []byte {
	return box("trak", concat(
		tkhd(t),
		box("mdia", concat(mdhd(t), hdlr(t), minf(t))),
	))
}

func moov(tracks []TrackSpec) []byte {
	parts := [][]byte{mvhd()}
	for _, t := range tracks {
		parts = append(parts, trak(t))
	}
	// mvex/trex so parsers accept the fragmented movie.
	var trex [][]byte
	for _, t := range tracks {
		trex = append(trex, fullBox("trex", 0, 0, concat(
			u32(t.ID), u32(1), u32(0), u32(0), u32(0),
		)))
	}
	parts = append(parts, box("mvex", concat(trex...)))
	return box("moov", concat(parts...))
}
