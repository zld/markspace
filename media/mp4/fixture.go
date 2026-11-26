package mp4

import (
	"bytes"
	"encoding/binary"

	gomp4 "github.com/abema/go-mp4"
)

// Builder assembles a fragmented MP4 byte stream for tests and fixtures.
// It writes only what the scanner and indexer must understand: ftyp, a video
// moov with timing and defaults, and one moof+mdat pair per fragment.
type Builder struct {
	Timescale  uint32
	TrackID    uint32
	FrameTicks uint32
	Width      uint16
	Height     uint16
	Buf        bytes.Buffer
	// dataPos is patched into every trun once the mdat offset is known.
	fragments []fragment
}

type fragment struct {
	moofStart int
	trunStart int
	samples   []SampleSpec
}

// SampleSpec describes one sample placed into a fragment's mdat.
type SampleSpec struct {
	Duration uint32
	Payload  []byte
	Key      bool
}

// NewBuilder returns a builder with sane fMP4 defaults.
func NewBuilder(timescale, trackID, frameTicks uint32) *Builder {
	return &Builder{Timescale: timescale, TrackID: trackID, FrameTicks: frameTicks, Width: 320, Height: 180}
}

func marshalBox(b gomp4.IImmutableBox) []byte {
	var buf bytes.Buffer
	if _, err := gomp4.Marshal(&buf, b, gomp4.Context{}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func wrap(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// WriteInit appends ftyp and moov.
func (b *Builder) WriteInit() {
	ftyp := &gomp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', '5'},
		MinorVersion: 512,
		CompatibleBrands: []gomp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '6'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	mvhd := &gomp4.Mvhd{Timescale: 1000, Rate: 0x00010000, Volume: 0x0100,
		Matrix: identityMatrix(), NextTrackID: b.TrackID + 1}
	mdhd := &gomp4.Mdhd{Timescale: b.Timescale, Language: [3]byte{'u', 'n', 'd'}}
	hdlr := &gomp4.Hdlr{HandlerType: gomp4.StrToBoxType("vide"), Name: "VideoHandler\x00"}
	tkhd := &gomp4.Tkhd{TrackID: b.TrackID, Width: uint32(b.Width) << 16, Height: uint32(b.Height) << 16,
		Matrix: identityMatrix()}
	vmhd := &gomp4.Vmhd{}
	trex := &gomp4.Trex{TrackID: b.TrackID, DefaultSampleDescriptionIndex: 1,
		DefaultSampleDuration: b.FrameTicks, DefaultSampleFlags: 0x01010000}
	stsd := append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, sampleEntry(b.Width, b.Height)...)
	empty := []byte{0, 0, 0, 0, 0, 0, 0, 0}

	moov := wrap("moov", concat(
		wrap("mvhd", marshalBox(mvhd)),
		wrap("trak", concat(
			wrap("tkhd", marshalBox(tkhd)),
			wrap("mdia", concat(
				wrap("mdhd", marshalBox(mdhd)),
				wrap("hdlr", marshalBox(hdlr)),
				wrap("minf", concat(
					wrap("vmhd", marshalBox(vmhd)),
					wrap("dinf", wrap("dref", concat([]byte{0, 0, 0, 0, 0, 0, 0, 1}, wrap("url ", []byte{0, 0, 0, 1})))),
					wrap("stbl", concat(
						wrap("stsd", stsd),
						wrap("stts", empty),
						wrap("stsc", empty),
						wrap("stsz", []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}),
						wrap("stco", empty),
					)),
				)),
			)),
		)),
		wrap("mvex", wrap("trex", marshalBox(trex))),
	))
	b.Buf.Write(wrap("ftyp", marshalBox(ftyp)))
	b.Buf.Write(moov)
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

// sampleEntry writes a minimal avc1 visual sample entry with an avcC child.
func sampleEntry(width, height uint16) []byte {
	entry := make([]byte, 78)
	binary.BigEndian.PutUint16(entry[6:], 1) // data_reference_index
	binary.BigEndian.PutUint16(entry[24:], width)
	binary.BigEndian.PutUint16(entry[26:], height)
	binary.BigEndian.PutUint32(entry[28:], 0x00480000) // 72 dpi
	binary.BigEndian.PutUint32(entry[32:], 0x00480000)
	binary.BigEndian.PutUint16(entry[40:], 1) // frame_count
	copy(entry[42:], []byte("replaykit"))
	entry[74] = 0x18 // depth
	avcC := wrap("avcC", []byte{1, 0x42, 0x00, 0x1e, 0xff, 0xe1, 0, 0, 0})
	return concat(entry, avcC)
}

// AddFragment appends one moof+mdat with the given samples.
func (b *Builder) AddFragment(decodeTime uint64, seq uint32, samples []SampleSpec) {
	frag := fragment{moofStart: b.Buf.Len()}

	mfhd := &gomp4.Mfhd{SequenceNumber: seq}
	tfhd := &gomp4.Tfhd{TrackID: b.TrackID}
	tfhd.SetFlags(gomp4.TfhdDefaultBaseIsMoof)
	tfdt := &gomp4.Tfdt{BaseMediaDecodeTimeV1: decodeTime}
	tfdt.SetVersion(1)

	entries := make([]gomp4.TrunEntry, len(samples))
	for i, s := range samples {
		flags := uint32(0x01010000) // non-sync
		if s.Key {
			flags = 0x02000000
		}
		entries[i] = gomp4.TrunEntry{SampleDuration: s.Duration, SampleSize: uint32(len(s.Payload)), SampleFlags: flags}
	}
	trun := &gomp4.Trun{SampleCount: uint32(len(samples)), DataOffset: 0, Entries: entries}
	trun.SetFlags(0x000701) // data-offset, duration, size, flags

	build := func(dataOffset int32) []byte {
		trun.DataOffset = dataOffset
		traf := wrap("traf", concat(
			wrap("tfhd", marshalBox(tfhd)),
			wrap("tfdt", marshalBox(tfdt)),
			wrap("trun", marshalBox(trun)),
		))
		return wrap("moof", concat(wrap("mfhd", marshalBox(mfhd)), traf))
	}
	moof := build(0)
	mdatPayload := 0
	for _, s := range samples {
		mdatPayload += len(s.Payload)
	}
	// The trun data offset points from the start of the moof to mdat's payload.
	moof = build(int32(len(moof) + 8))
	b.Buf.Write(moof)
	b.Buf.Write(wrap("mdat", make([]byte, mdatPayload)))
	// Fill mdat payload with the sample bytes at known absolute positions.
	mdatPayloadStart := frag.moofStart + len(moof) + 8
	copy(b.Buf.Bytes()[mdatPayloadStart:], flatten(samples))

	frag.samples = samples
	b.fragments = append(b.fragments, frag)
}

func flatten(samples []SampleSpec) []byte {
	var out []byte
	for _, s := range samples {
		out = append(out, s.Payload...)
	}
	return out
}

// Bytes returns the assembled stream.
func (b *Builder) Bytes() []byte { return b.Buf.Bytes() }

// TruncateLast cuts n bytes off the end, producing an incomplete fragment.
func (b *Builder) TruncateLast(n int) {
	data := b.Buf.Bytes()
	if n > len(data) {
		n = len(data)
	}
	b.Buf.Truncate(len(data) - n)
}

// LastFragmentDataStart reports the absolute mdat payload offset and the
// fragment index of the most recent fragment, for targeted corruption tests.
func (b *Builder) LastFragmentDataStart() (int, bool) {
	if len(b.fragments) == 0 {
		return 0, false
	}
	f := b.fragments[len(b.fragments)-1]
	return f.moofStart, true
}
