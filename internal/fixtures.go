package continuity

import (
	"bytes"
	"fmt"
)

// The fixture builder emits deterministic fragmented-MP4 (CMAF) bytes using
// hand-rolled big-endian writers. Keeping the writer local (rather than using
// go-mp4's writer) makes the fixtures independent of library output details
// and keeps damaged fixtures byte-exact.

// Box is a parsed ISOBMFF box with its absolute byte extents in its segment.
type Box struct {
	Type    string
	Offset  int64
	Size    int64
	Header  int64
	Payload []byte
}

// End returns the exclusive end offset of the box.
func (b Box) End() int64 { return b.Offset + b.Size }

// Boxes returns the top-level boxes of a segment, in byte order.
func Boxes(data []byte) ([]Box, error) {
	var out []Box
	for off := int64(0); off < int64(len(data)); {
		if int64(len(data))-off < 8 {
			return out, fmt.Errorf("truncated box header at %d (%d trailing bytes)", off, int64(len(data))-off)
		}
		size := int64(be32(data[off:]))
		typ := string(data[off+4 : off+8])
		hdr := int64(8)
		if size == 1 {
			if int64(len(data))-off < 16 {
				return out, fmt.Errorf("truncated 64-bit box header at %d", off)
			}
			size = int64(be64(data[off+8:]))
			hdr = 16
		} else if size == 0 {
			size = int64(len(data)) - off
		}
		if size < hdr {
			return out, fmt.Errorf("box %q at %d has invalid size %d", typ, off, size)
		}
		if off+size > int64(len(data)) {
			return out, fmt.Errorf("box %q at %d declares %d bytes but only %d remain", typ, off, size, int64(len(data))-off)
		}
		out = append(out, Box{Type: typ, Offset: off, Size: size, Header: hdr, Payload: data[off+hdr : off+size]})
		off += size
	}
	return out, nil
}

type boxWriter struct{ buf bytes.Buffer }

func (w *boxWriter) box(typ string, payload []byte) {
	w.u32(uint32(8 + len(payload)))
	w.raw([]byte(typ))
	w.raw(payload)
}

func (w *boxWriter) fullBox(typ string, version byte, flags uint32, payload []byte) {
	w.box(typ, fullBoxPayload(version, flags, payload))
}

func fullBoxPayload(version byte, flags uint32, payload []byte) []byte {
	b := make([]byte, 4, 4+len(payload))
	b[0] = version
	b[1] = byte(flags >> 16)
	b[2] = byte(flags >> 8)
	b[3] = byte(flags)
	return append(b, payload...)
}

func (w *boxWriter) raw(b []byte) { w.buf.Write(b) }
func (w *boxWriter) u8(v byte)    { w.buf.WriteByte(v) }
func (w *boxWriter) u16(v uint16) { w.buf.Write([]byte{byte(v >> 8), byte(v)}) }
func (w *boxWriter) u24(v uint32) { w.buf.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)}) }
func (w *boxWriter) u32(v uint32) {
	w.buf.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
func (w *boxWriter) u64(v uint64)  { w.u32(uint32(v >> 32)); w.u32(uint32(v)) }
func (w *boxWriter) zeros(n int)   { w.buf.Write(make([]byte, n)) }
func (w *boxWriter) bytes() []byte { return w.buf.Bytes() }
func (w *boxWriter) len() int      { return w.buf.Len() }

func be16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func be64(b []byte) uint64 { return uint64(be32(b))<<32 | uint64(be32(b[4:])) }

func be32Put(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

// TrackSpec describes one track emitted by the fixture builder.
type TrackSpec struct {
	// ID is the track_ID used in the MP4 boxes.
	ID uint32
	// Kind is audio or video.
	Kind MediaKind
	// Timescale is the mdhd/tfdt time base.
	Timescale uint32
	// Handler is the mdia handler type: "vide" or "soun".
	Handler string
	// SampleEntry is the stsd entry type, e.g. "avc1" or "mp4a".
	SampleEntry string
	// CodecConfig is the raw codec configuration (avcC payload, or the
	// DecoderSpecificInfo for mp4a). Empty means none is emitted.
	CodecConfig []byte
	// Width and Height are the visual sample entry dimensions.
	Width, Height uint16
	// ChannelCount and SampleRate describe audio sample entries.
	ChannelCount uint16
	SampleRate   uint32
}

// SampleSpec is one raw sample to place in a fragment.
type SampleSpec struct {
	// Data is the compressed sample payload, copied verbatim.
	Data []byte
	// Duration is in the track timescale.
	Duration uint32
	// CompositionOffset is the signed composition-time offset in the track
	// timescale (PTS - DTS); zero for audio and for video without reordering.
	CompositionOffset int32
	// Sync marks a random-access point.
	Sync bool
}

// InitSpec describes an init segment carrying ftyp+moov.
type InitSpec struct {
	MajorBrand       string
	MinorVersion     uint32
	CompatibleBrands []string
	MovieTimescale   uint32
	Tracks           []TrackSpec
	// OmitMoov emits only ftyp, for missing-init fixtures.
	OmitMoov bool
}

// FragmentSpec describes one moof+mdat fragment.
type FragmentSpec struct {
	// BaseDecodeTime is in Timescale units.
	BaseDecodeTime uint64
	// TrackID must match an init segment track.
	TrackID uint32
	// Timescale is the track timescale used by tfdt/trun.
	Timescale uint32
	// DefaultSampleDuration is the tfhd default; per-sample durations win.
	DefaultSampleDuration uint32
	Samples               []SampleSpec
	SequenceNumber        uint32
	// TruncateBytes drops that many trailing bytes from mdat while the mdat
	// header still declares the full size (truncated-box fixture).
	TruncateBytes int
}

// BuildInit renders ftyp+moov for the given tracks.
func BuildInit(spec InitSpec) []byte {
	if spec.MajorBrand == "" {
		spec.MajorBrand = "iso6"
	}
	if spec.MovieTimescale == 0 {
		spec.MovieTimescale = BaseTimescale
	}
	brands := spec.CompatibleBrands
	if len(brands) == 0 {
		brands = []string{"iso6", "iso5", "mp41"}
	}
	out := &boxWriter{}
	fw := &boxWriter{}
	fw.raw([]byte(pad4(spec.MajorBrand)))
	fw.u32(spec.MinorVersion)
	for _, b := range brands {
		fw.raw([]byte(pad4(b)))
	}
	out.box("ftyp", fw.bytes())
	if spec.OmitMoov {
		return out.bytes()
	}
	moov := &boxWriter{}
	mv := &boxWriter{}
	mv.u32(0) // creation_time
	mv.u32(0) // modification_time
	mv.u32(spec.MovieTimescale)
	mv.u32(0) // duration: fragmented
	mv.u32(0x00010000)
	mv.u16(0x0100)
	mv.u16(0)
	mv.u32(0)
	mv.u32(0)
	writeMatrix(mv)
	mv.zeros(24)
	mv.u32(uint32(len(spec.Tracks)) + 1) // next_track_ID
	moov.fullBox("mvhd", 0, 0, mv.bytes())
	for _, ts := range spec.Tracks {
		moov.raw(buildTrak(ts))
	}
	mvex := &boxWriter{}
	for _, ts := range spec.Tracks {
		trex := &boxWriter{}
		trex.u32(ts.ID)
		trex.u32(1) // default_sample_description_index
		trex.u32(0) // default_sample_duration
		trex.u32(0) // default_sample_size
		trex.u32(0) // default_sample_flags
		mvex.fullBox("trex", 0, 0, trex.bytes())
	}
	moov.box("mvex", mvex.bytes())
	out.box("moov", moov.bytes())
	return out.bytes()
}

func writeMatrix(w *boxWriter) {
	for _, m := range []int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		w.u32(uint32(m))
	}
}

func buildTrak(ts TrackSpec) []byte {
	trak := &boxWriter{}
	tk := &boxWriter{}
	tk.u32(0) // creation_time
	tk.u32(0) // modification_time
	tk.u32(ts.ID)
	tk.u32(0) // reserved
	tk.u32(0) // duration
	tk.u32(0)
	tk.u32(0)
	tk.u16(0) // layer
	tk.u16(0) // alternate_group
	if ts.Kind == MediaAudio {
		tk.u16(0x0100)
	} else {
		tk.u16(0)
	}
	tk.u16(0)
	writeMatrix(tk)
	if ts.Kind == MediaVideo {
		tk.u32(uint32(ts.Width) << 16)
		tk.u32(uint32(ts.Height) << 16)
	} else {
		tk.u32(0)
		tk.u32(0)
	}
	trak.fullBox("tkhd", 0, 0x000007, tk.bytes())

	mdia := &boxWriter{}
	md := &boxWriter{}
	md.u32(0) // creation_time
	md.u32(0) // modification_time
	md.u32(ts.Timescale)
	md.u32(0) // duration
	md.u16(packLanguage("und"))
	md.u16(0)
	mdia.fullBox("mdhd", 0, 0, md.bytes())

	handler := ts.Handler
	if handler == "" {
		if ts.Kind == MediaVideo {
			handler = "vide"
		} else {
			handler = "soun"
		}
	}
	hd := &boxWriter{}
	hd.u32(0)
	hd.raw([]byte(pad4(handler)))
	hd.zeros(12)
	if ts.Kind == MediaVideo {
		hd.raw([]byte("VideoHandler\x00"))
	} else {
		hd.raw([]byte("SoundHandler\x00"))
	}
	mdia.fullBox("hdlr", 0, 0, hd.bytes())

	minf := &boxWriter{}
	if ts.Kind == MediaVideo {
		vm := &boxWriter{}
		vm.u16(0)
		vm.zeros(6)
		minf.fullBox("vmhd", 0, 0x000001, vm.bytes())
	} else {
		sm := &boxWriter{}
		sm.u16(0)
		sm.u16(0)
		minf.fullBox("smhd", 0, 0, sm.bytes())
	}
	dref := &boxWriter{}
	dref.u32(1)
	dref.fullBox("url ", 0, 0x000001, nil)
	dinf := &boxWriter{}
	dinf.fullBox("dref", 0, 0, dref.bytes())
	minf.box("dinf", dinf.bytes())
	minf.raw(buildStbl(ts))
	mdia.box("minf", minf.bytes())
	trak.box("mdia", mdia.bytes())
	out := &boxWriter{}
	out.box("trak", trak.bytes())
	return out.bytes()
}

func buildStbl(ts TrackSpec) []byte {
	stbl := &boxWriter{}
	stsd := &boxWriter{}
	stsd.u32(1)
	stsd.raw(buildSampleEntry(ts))
	stbl.fullBox("stsd", 0, 0, stsd.bytes())
	for _, typ := range []string{"stts", "stsc", "stsz", "stco"} {
		tbl := &boxWriter{}
		tbl.u32(0)
		if typ == "stsz" {
			tbl.u32(0) // sample_size
		}
		stbl.fullBox(typ, 0, 0, tbl.bytes())
	}
	out := &boxWriter{}
	out.box("stbl", stbl.bytes())
	return out.bytes()
}

func buildSampleEntry(ts TrackSpec) []byte {
	entryType := ts.SampleEntry
	if entryType == "" {
		if ts.Kind == MediaVideo {
			entryType = "avc1"
		} else {
			entryType = "mp4a"
		}
	}
	e := &boxWriter{}
	e.zeros(6)
	e.u16(1) // data_reference_index
	if ts.Kind == MediaVideo {
		e.u16(0)
		e.u16(0)
		e.zeros(12)
		e.u16(ts.Width)
		e.u16(ts.Height)
		e.u32(0x00480000)
		e.u32(0x00480000)
		e.u32(0)
		e.u16(1)
		e.zeros(32)
		e.u16(0x0018)
		e.u16(0xFFFF)
		if len(ts.CodecConfig) > 0 {
			e.box("avcC", ts.CodecConfig)
		}
	} else {
		e.u16(0) // version
		e.u16(0) // revision
		e.u32(0) // vendor
		e.u16(ts.ChannelCount)
		e.u16(16)
		e.u16(0)
		e.u16(0)
		e.u32(ts.SampleRate << 16)
		if len(ts.CodecConfig) > 0 {
			e.fullBox("esds", 0, 0, buildESDS(ts.CodecConfig))
		}
	}
	out := &boxWriter{}
	out.box(entryType, e.bytes())
	return out.bytes()
}

func buildESDS(dsi []byte) []byte {
	dsiDesc := append([]byte{0x05, byte(len(dsi))}, dsi...)
	dcdBody := []byte{0x40, 0x15, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	dcdBody = append(dcdBody, dsiDesc...)
	dcd := append([]byte{0x04, byte(len(dcdBody))}, dcdBody...)
	sl := []byte{0x06, 0x01, 0x02}
	esBody := []byte{0x00, 0x01, 0x00}
	esBody = append(esBody, dcd...)
	esBody = append(esBody, sl...)
	es := append([]byte{0x03, byte(len(esBody))}, esBody...)
	return es
}

func packLanguage(lang string) uint16 {
	for len(lang) < 3 {
		lang += " "
	}
	var v uint16
	for i := 0; i < 3; i++ {
		c := lang[i]
		if c < 0x61 || c > 0x7a {
			c = ' ' // pack ' ' as 0
			v |= 0 << (10 - 5*uint(i))
			continue
		}
		v |= uint16(c-0x60) << (10 - 5*uint(i))
	}
	return v
}

// BuildFragment renders moof+mdat for the given samples.
func BuildFragment(spec FragmentSpec) []byte {
	if spec.SequenceNumber == 0 {
		spec.SequenceNumber = 1
	}
	mfhd := &boxWriter{}
	mfhd.u32(spec.SequenceNumber)
	mfhdBox := &boxWriter{}
	mfhdBox.fullBox("mfhd", 0, 0, mfhd.bytes())

	tfhd := &boxWriter{}
	tfhd.u32(spec.TrackID)
	tfhd.u32(0) // base_data_offset (unused with default-base-is-moof)
	tfhd.u32(0) // sample_description_index
	tfhd.u32(spec.DefaultSampleDuration)
	tfhd.u32(1) // default_sample_size (per-sample sizes override)
	const tfhdFlags = 0x000002 | 0x000008 | 0x000010 | 0x020000
	tfhdBox := &boxWriter{}
	tfhdBox.fullBox("tfhd", 0, tfhdFlags, tfhd.bytes())

	tfdt := &boxWriter{}
	tfdt.u64(spec.BaseDecodeTime)
	tfdtBox := &boxWriter{}
	tfdtBox.fullBox("tfdt", 1, 0, tfdt.bytes())

	trun := &boxWriter{}
	trun.u32(uint32(len(spec.Samples)))
	trun.u32(0) // data_offset, patched once the moof size is known
	const trunFlags = 0x000001 | 0x000100 | 0x000200 | 0x000400 | 0x000800
	for _, s := range spec.Samples {
		trun.u32(s.Duration)
		trun.u32(uint32(len(s.Data)))
		if s.Sync {
			trun.u32(flagSync)
		} else {
			trun.u32(flagNonSync)
		}
		trun.u32(uint32(s.CompositionOffset))
	}
	trunBox := &boxWriter{}
	trunBox.fullBox("trun", 1, trunFlags, trun.bytes())

	traf := &boxWriter{}
	traf.raw(tfhdBox.bytes())
	traf.raw(tfdtBox.bytes())
	traf.raw(trunBox.bytes())
	trafBox := &boxWriter{}
	trafBox.box("traf", traf.bytes())

	moofBody := &boxWriter{}
	moofBody.raw(mfhdBox.bytes())
	moofBody.raw(trafBox.bytes())
	moof := &boxWriter{}
	moof.box("moof", moofBody.bytes())
	moofBytes := moof.bytes()

	// Locate trun's data_offset field: trun box starts after moof header,
	// mfhd, traf header, tfhd and tfdt; its payload begins 8 bytes later with
	// version/flags (4) then sample_count (4).
	trunStart := 8 + mfhdBox.len() + 8 + tfhdBox.len() + tfdtBox.len()
	dataOffsetField := trunStart + 8 + 4 + 4
	dataOffset := int32(len(moofBytes) + 8)
	be32Put(moofBytes[dataOffsetField:dataOffsetField+4], uint32(dataOffset))

	total := 0
	for _, s := range spec.Samples {
		total += len(s.Data)
	}
	body := &boxWriter{}
	body.u32(uint32(8 + total))
	body.raw([]byte("mdat"))
	for _, s := range spec.Samples {
		body.raw(s.Data)
	}
	mdat := body.bytes()
	if spec.TruncateBytes > 0 {
		if spec.TruncateBytes >= len(spec.Samples) && spec.TruncateBytes > total {
			spec.TruncateBytes = total
		}
		if spec.TruncateBytes > len(mdat)-8 {
			spec.TruncateBytes = len(mdat) - 8
		}
		mdat = mdat[:len(mdat)-spec.TruncateBytes]
	}
	out := &boxWriter{}
	out.raw(moofBytes)
	out.raw(mdat)
	return out.bytes()
}

const (
	flagSync    = 0x02000000 // sample_depends_on = 2 (I frame)
	flagNonSync = 0x01010000 // depends_on = 1, is_non_sync = 1
)

// pad4 right-pads or truncates s to exactly 4 bytes.
func pad4(s string) string {
	b := []byte(s)
	for len(b) < 4 {
		b = append(b, ' ')
	}
	return string(b[:4])
}

// pattern returns n deterministic bytes seeded by seed. The sequence is stable
// across runs and architectures.
func pattern(seed byte, n int) []byte {
	out := make([]byte, n)
	x := uint32(seed)<<24 | 0x9E3779B9
	for i := range out {
		x = x*1664525 + 1013904223
		out[i] = byte(x >> 24)
	}
	return out
}

// avcCConfig returns a minimal, valid avcC payload for a 4-byte NAL length
// stream.
func avcCConfig() []byte {
	return []byte{
		0x01, 0x42, 0x00, 0x1F, 0xFF, 0xE1,
		0x00, 0x04, 0x67, 0x42, 0x00, 0x1F,
		0x01, 0x00, 0x03, 0x68, 0xCE, 0x3C,
	}
}
