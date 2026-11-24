// Package fixture builds self-contained sample media in memory: a valid
// MP4/fMP4 with multiple timescales and cross-track timed metadata, plus the
// deliberately damaged variants the demo and tests exercise.
package fixture

import (
	"bytes"
)

// Media description ------------------------------------------------------

type sampleSpec struct {
	data  []byte
	delta uint32
	cts   int64
	sync  bool
}

type trackSpec struct {
	id        uint32
	kind      string
	handler   string
	timescale uint32
	stsd      []byte
	samples   []sampleSpec
}

// Sample set sizes.  Small payloads keep the fixtures readable while still
// exercising multi-track offsets.
var (
	videoPayload   = bytes.Repeat([]byte{0x00, 0x00, 0x00, 0x06, 0x05}, 4) // 4 length-prefixed SEI NALs
	audioPayload   = []byte{0x21, 0x10, 0x05, 0x20, 0x74, 0x65, 0x73, 0x74}
	subPayload     = []byte{0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	chapterPayload = []byte{0x00, 0x07, 'c', 'h', 'a', 'p', 't', 'e', 'r'}
	id3Payload     = buildID3("TIT2", []byte("fixture"))
)

func buildID3(frameID string, text []byte) []byte {
	var b wbuf
	b.str("ID3")
	b.u8(4)
	b.u8(0)
	b.u8(0)
	size := 10 + len(text)
	b.u8(byte(size >> 21 & 0x7f))
	b.u8(byte(size >> 14 & 0x7f))
	b.u8(byte(size >> 7 & 0x7f))
	b.u8(byte(size & 0x7f))
	b.str(frameID)
	b.u32(uint32(len(text)))
	b.u16(0)
	b.u8(0)
	b.raw(text)
	return b.bytes()
}

func avcC() []byte {
	var b wbuf
	b.u8(1)
	b.u8(0x42)
	b.u8(0x00)
	b.u8(0x1f)
	b.u8(0xff) // lengthSizeMinusOne = 3
	b.u8(0xe1)
	b.u16(2)
	b.raw([]byte{0x67, 0x42})
	b.u8(1)
	b.u16(2)
	b.raw([]byte{0x68, 0xce})
	return box("avcC", b.bytes())
}

func visualSampleEntry(typ string, extra ...[]byte) []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	b.u16(0)
	b.u16(0)
	b.zeros(12)
	b.u16(1280)
	b.u16(720)
	b.u32(0x00480000)
	b.u32(0x00480000)
	b.u32(0)
	b.u16(1)
	b.zeros(32)
	b.u16(0x0018)
	b.u16(0xffff)
	for _, e := range extra {
		b.raw(e)
	}
	return box(typ, b.bytes())
}

func audioSampleEntry() []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	b.zeros(8)
	b.u16(2)
	b.u16(16)
	b.u16(0)
	b.u16(0)
	b.u32(48000 << 16)
	return box("mp4a", b.bytes())
}

func subtitleSampleEntry() []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	b.str("http://www.w3.org/ns/ttml")
	b.u8(0)
	b.str("")
	b.u8(0)
	b.str("")
	b.u8(0)
	return box("stpp", b.bytes())
}

func metadataSampleEntry(typ string) []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	return box(typ, b.bytes())
}

func stsd(entries ...[]byte) []byte {
	var b wbuf
	b.u32(uint32(len(entries)))
	for _, e := range entries {
		b.raw(e)
	}
	return fullbox("stsd", 0, 0, b.bytes())
}

// seiPayload builds one user_data_unregistered SEI NAL plus filler NALs so the
// video sample exercises the length-prefixed SEI path rather than a single NAL.
func seiPayload(uuid string) []byte {
	msg := append([]byte(uuid), []byte("meta")...)
	var rbsp []byte
	rbsp = append(rbsp, 0x05)
	rbsp = append(rbsp, byte(len(msg)))
	rbsp = append(rbsp, msg...)
	sei := append([]byte{0x06}, rbsp...)
	var out wbuf
	out.u32(uint32(len(sei)))
	out.raw(sei)
	filler := []byte{0x09, 0xf0}
	out.u32(uint32(len(filler)))
	out.raw(filler)
	return out.bytes()
}

// Options controls the generated valid fixture.
type Options struct {
	// EventDuplicates adds a duplicate emsg with identical content and time so
	// the dedup result can be asserted as exactly one kept copy.
	EventDuplicates bool
	// Fragmented produces a moof/mdat file with a mfra index instead of a
	// classic sample table.
	Fragmented bool
	// DropIndex removes the index from a fragmented file.
	DropIndex bool
}

// BuildValid returns the normal multi-track fixture.
func BuildValid(opts Options) Payload {
	tracks := []trackSpec{
		{
			id: 1, kind: "video", handler: "vide", timescale: 90000,
			stsd: stsd(visualSampleEntry("avc1", avcC())),
			samples: []sampleSpec{
				{data: seiPayload("mp-sei-1"), delta: 3000, sync: true},
				{data: seiPayload("mp-sei-2"), delta: 3000, sync: false},
				{data: seiPayload("mp-sei-3"), delta: 3000, sync: true},
			},
		},
		{
			id: 2, kind: "audio", handler: "soun", timescale: 48000,
			stsd:    stsd(audioSampleEntry()),
			samples: []sampleSpec{{data: audioPayload, delta: 1024, sync: true}, {data: audioPayload, delta: 1024, sync: true}},
		},
		{
			id: 3, kind: "subtitle", handler: "subt", timescale: 1000,
			stsd:    stsd(subtitleSampleEntry()),
			samples: []sampleSpec{{data: subPayload, delta: 500, sync: true}, {data: subPayload, delta: 500, sync: true}},
		},
		{
			id: 4, kind: "chapter", handler: "text", timescale: 1000,
			stsd:    stsd(metadataSampleEntry("tx3g")),
			samples: []sampleSpec{{data: chapterPayload, delta: 1000, sync: true}},
		},
		{
			id: 5, kind: "metadata", handler: "meta", timescale: 1000,
			stsd:    stsd(metadataSampleEntry("ID3 ")),
			samples: []sampleSpec{{data: id3Payload, delta: 1000, sync: true}},
		},
	}

	if opts.Fragmented {
		return Payload{Name: "valid-fragmented.mp4", Data: buildFragmented(tracks, opts)}
	}
	return Payload{Name: "valid.mp4", Data: buildClassic(tracks, opts)}
}

func emsgFixtureBoxes(opts Options) [][]byte {
	events := []struct {
		scheme, value string
		time          uint64
		data          string
	}{
		{"urn:mp:cue", "1", 2000, "cue-a"},
		{"urn:mp:cue", "2", 1000, "cue-b"},
	}
	if opts.EventDuplicates {
		events = append(events, events[0])
	}
	var out [][]byte
	for _, e := range events {
		var b wbuf
		b.full(1, 0)
		b.u32(1000)
		b.u64(e.time)
		b.u32(0)
		b.u32(0)
		b.str(e.scheme)
		b.u8(0)
		b.str(e.value)
		b.u8(0)
		b.str(e.data)
		out = append(out, box("emsg", b.bytes()))
	}
	return out
}

func udtaChapterBox() []byte {
	var b wbuf
	b.full(0, 0)
	b.u8(0)
	b.u32(1)
	b.u64(0)
	b.u8(1)
	b.str("chapter")
	b.u8(0)
	chpl := box("chpl", b.bytes())
	return container("udta", chpl)
}
