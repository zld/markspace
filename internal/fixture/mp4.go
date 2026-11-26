package fixture

import (
	"math/rand"

	"github.com/abema/go-mp4"
)

// TrackSpec describes one generated track: its ID, timescale and whether it is
// video (so its samples carry an AVC NAL prefix and its init a minimal avcC).
type TrackSpec struct {
	ID        uint32
	Timescale uint32
	Video     bool
	Width     uint16
	Height    uint16
}

// SampleSpec is one sample to place in a fragment.
type SampleSpec struct {
	// DurTicks is the decode duration in the track timescale.
	DurTicks int64
	// Size is the byte size of the generated payload.
	Size int
	// Key marks a sync sample.
	Key bool
}

// buildInit returns the initialization segment for a single track.
func buildInit(t TrackSpec) []byte {
	ftyp := &mp4.Ftyp{Box: mp4.Box{}, MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 512}
	ftyp.CompatibleBrands = []mp4.CompatibleBrandElem{
		{CompatibleBrand: [4]byte{'i', 's', 'o', '6'}},
		{CompatibleBrand: [4]byte{'d', 'a', 's', 'h'}},
	}

	mvhd := &mp4.Mvhd{
		FullBox: mp4.FullBox{}, Timescale: t.Timescale, Rate: 0x00010000, Volume: 0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: t.ID + 1,
	}
	tkhd := &mp4.Tkhd{
		FullBox: mp4.FullBox{}, TrackID: t.ID,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}
	tkhd.SetFlags(0x7)
	if t.Video {
		tkhd.Width = uint32(t.Width) << 16
		tkhd.Height = uint32(t.Height) << 16
	} else {
		tkhd.Volume = 0x0100
	}
	mdhd := &mp4.Mdhd{FullBox: mp4.FullBox{}, Timescale: t.Timescale}
	mdhd.Language = [3]byte{'u' - 0x60, 'n' - 0x60, 'd' - 0x60}
	hdlr := &mp4.Hdlr{FullBox: mp4.FullBox{}, HandlerType: [4]byte{'v', 'i', 'd', 'e'}}
	if !t.Video {
		hdlr.HandlerType = [4]byte{'s', 'o', 'u', 'n'}
	}

	stbl := container(mp4.BoxTypeStbl(),
		buildStsd(t),
		leaf(&mp4.Stts{FullBox: mp4.FullBox{}}),
		leaf(&mp4.Stsc{FullBox: mp4.FullBox{}}),
		leaf(&mp4.Stsz{FullBox: mp4.FullBox{}}),
		leaf(&mp4.Stco{FullBox: mp4.FullBox{}}),
	)
	minf := container(mp4.BoxTypeMinf(),
		buildMediaHeader(t),
		buildDinf(),
		stbl,
	)
	mdia := container(mp4.BoxTypeMdia(), leaf(mdhd), leaf(hdlr), minf)
	trak := container(mp4.BoxTypeTrak(), leaf(tkhd), mdia)
	mvex := container(mp4.BoxTypeMvex(), leaf(&mp4.Trex{
		FullBox: mp4.FullBox{}, TrackID: t.ID, DefaultSampleDescriptionIndex: 1,
		DefaultSampleFlags: 0x01010000,
	}))
	moov := container(mp4.BoxTypeMoov(), leaf(mvhd), trak, mvex)
	return concat(leaf(ftyp).bytes(), moov.bytes())
}

// buildMediaHeader writes vmhd for video and smhd for audio.
func buildMediaHeader(t TrackSpec) box {
	if t.Video {
		v := &mp4.Vmhd{FullBox: mp4.FullBox{}}
		v.SetFlags(1)
		return leaf(v)
	}
	return leaf(&mp4.Smhd{FullBox: mp4.FullBox{}})
}

// buildDinf writes a self-contained data reference.
func buildDinf() box {
	url := &mp4.Url{FullBox: mp4.FullBox{}}
	url.SetFlags(1)
	drefPayload := concat(u32(0), u32(1), leaf(url).bytes())
	return box{typ: mp4.BoxTypeDinf(), data: box{typ: mp4.BoxTypeDref(), data: drefPayload}.bytes()}
}

// buildStsd writes the sample description for the track.
func buildStsd(t TrackSpec) box {
	var entry box
	if t.Video {
		avcC := &mp4.AVCDecoderConfiguration{
			AnyTypeBox:           mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
			ConfigurationVersion: 1, Profile: 0x42, Level: 0x1f, LengthSizeMinusOne: 3,
		}
		avcC.NumOfSequenceParameterSets = 1
		avcC.SequenceParameterSets = []mp4.AVCParameterSet{{Length: 3, NALUnit: []byte{0x67, 0x42, 0x1f}}}
		avcC.NumOfPictureParameterSets = 1
		avcC.PictureParameterSets = []mp4.AVCParameterSet{{Length: 2, NALUnit: []byte{0x68, 0xce}}}
		entry = withChildren(&mp4.VisualSampleEntry{
			SampleEntry:     mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()}, DataReferenceIndex: 1},
			Width:           t.Width,
			Height:          t.Height,
			Horizresolution: 0x00480000, Vertresolution: 0x00480000, FrameCount: 1, Depth: 0x0018,
		}, leaf(avcC))
	} else {
		// esds descriptors are nested one per message level: an ES
		// descriptor containing a DecoderConfig and a DecoderSpecificInfo.
		esds := &mp4.Esds{FullBox: mp4.FullBox{}, Descriptors: []mp4.Descriptor{
			{Tag: 0x03, ESDescriptor: &mp4.ESDescriptor{ESID: 1}},
			{Tag: 0x04, DecoderConfigDescriptor: &mp4.DecoderConfigDescriptor{
				ObjectTypeIndication: 0x40, StreamType: 0x05,
				MaxBitrate: 128000, AvgBitrate: 128000,
			}},
			{Tag: 0x05, Size: 2, Data: []byte{0x11, 0x90}},
		}}
		entry = withChildren(&mp4.AudioSampleEntry{
			SampleEntry:  mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()}, DataReferenceIndex: 1},
			ChannelCount: 2, SampleSize: 16, SampleRate: 48000 << 16,
		}, leaf(esds))
	}
	// stsd payload: version+flags, entry_count, then the entry box.
	return box{typ: mp4.BoxTypeStsd(), data: concat(u32(0), u32(1), entry.bytes())}
}

// buildSegment returns one fMP4 media fragment: styp, moof and mdat.
// baseDecodeTime is in the track timescale.
func buildSegment(t TrackSpec, seq uint32, baseDecodeTime int64, samples []SampleSpec, seed int64) []byte {
	styp := &mp4.Styp{Box: mp4.Box{}, MajorBrand: [4]byte{'m', 's', 'd', 'h'}}
	styp.CompatibleBrands = []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'m', 's', 'd', 'h'}}}

	trun := &mp4.Trun{FullBox: mp4.FullBox{}, SampleCount: uint32(len(samples))}
	trun.SetFlags(0x000001 | 0x000100 | 0x000200 | 0x000400)
	for i, s := range samples {
		flags := uint32(0x01010000) // sample_is_non_sync_sample
		if s.Key || (!t.Video && i == 0) {
			flags = 0x02000000 // sample_is_non_sync_sample=0, sync
		}
		trun.Entries = append(trun.Entries, mp4.TrunEntry{
			SampleDuration: uint32(s.DurTicks),
			SampleSize:     uint32(s.Size),
			SampleFlags:    flags,
		})
	}
	tfhd := &mp4.Tfhd{FullBox: mp4.FullBox{}, TrackID: t.ID}
	tfhd.SetFlags(0x020000) // default-base-is-moof
	tfdt := &mp4.Tfdt{FullBox: mp4.FullBox{}}
	tfdt.SetVersion(1)
	tfdt.BaseMediaDecodeTimeV1 = uint64(baseDecodeTime)

	// Two-pass: build the moof with a placeholder data offset to learn its
	// size, then rebuild with the true offset (moof + mdat header).
	buildMoof := func(dataOffset int32) box {
		trun.DataOffset = dataOffset
		return container(mp4.BoxTypeMoof(),
			leaf(&mp4.Mfhd{FullBox: mp4.FullBox{}, SequenceNumber: seq}),
			container(mp4.BoxTypeTraf(), leaf(tfhd), leaf(tfdt), leaf(trun)),
		)
	}
	first := buildMoof(0).bytes()
	moof := buildMoof(int32(len(first) + 8)).bytes()

	var payload []byte
	for i, s := range samples {
		payload = append(payload, deterministicBytes(seed, int64(seq), i, s.Size, s.Key)...)
	}
	mdat := box{typ: mp4.BoxTypeMdat(), data: payload}
	return concat(leaf(styp).bytes(), moof, mdat.bytes())
}

// deterministicBytes derives reproducible payload bytes from a sample
// identity and, for video, lays an AVC-style length-prefixed NAL whose type is
// IDR for keyframes. It is shaped data, not decodable video.
func deterministicBytes(seed, seg int64, idx, size int, key bool) []byte {
	out := make([]byte, size)
	r := rand.New(rand.NewSource(seed*1_000_003 + seg*7919 + int64(idx)*104729))
	r.Read(out)
	if size >= 5 {
		writeU32At(out, 0, uint32(size-4))
		out[4] = 0x41
		if key {
			out[4] = 0x65
		}
	}
	return out
}

func writeU32At(dst []byte, off int, v uint32) {
	dst[off] = byte(v >> 24)
	dst[off+1] = byte(v >> 16)
	dst[off+2] = byte(v >> 8)
	dst[off+3] = byte(v)
}

// SampleLayout describes one generated sample for later round-trip checks.
type SampleLayout struct {
	Offset   int64
	Size     int
	DTSTicks int64
	DurTicks int64
	Key      bool
}

// SegmentLayout records what a generated fragment contains, so a test or the
// verifier can compare a re-parse against the generator's intent.
type SegmentLayout struct {
	URI            string
	Init           string
	Representation string
	TrackID        uint32
	Timescale      uint32
	BaseDTSTicks   int64
	Samples        []SampleLayout
}

// fragment builds a media fragment and returns both its bytes and a layout
// describing where each sample landed. The layout is computed the same way a
// parser would, by walking the finished moof and mdat headers.
func fragment(t TrackSpec, seq uint32, baseDecodeTime int64, samples []SampleSpec, seed int64, uri, init, rep string) ([]byte, SegmentLayout) {
	data := buildSegment(t, seq, baseDecodeTime, samples, seed)
	lay := SegmentLayout{
		URI: uri, Init: init, Representation: rep,
		TrackID: t.ID, Timescale: t.Timescale, BaseDTSTicks: baseDecodeTime,
	}
	// Recover the moof size and mdat payload start by reading the first boxes,
	// which mirrors what a decoder does and keeps the layout honest.
	mdatPayload := mdatDataOffset(data)
	dts := baseDecodeTime
	off := mdatPayload
	for _, s := range samples {
		lay.Samples = append(lay.Samples, SampleLayout{
			Offset: off, Size: s.Size, DTSTicks: dts, DurTicks: s.DurTicks, Key: s.Key,
		})
		off += int64(s.Size)
		dts += s.DurTicks
	}
	return data, lay
}

// mdatDataOffset returns the byte offset of the mdat payload in a generated
// fragment (after styp, moof and the mdat header).
func mdatDataOffset(data []byte) int64 {
	off := int64(0)
	for off+8 <= int64(len(data)) {
		size := int64(uint32(data[off])<<24 | uint32(data[off+1])<<16 | uint32(data[off+2])<<8 | uint32(data[off+3]))
		typ := string(data[off+4 : off+8])
		if typ == "mdat" {
			return off + 8
		}
		if size < 8 {
			return off
		}
		off += size
	}
	return off
}

// Fragment returns the bytes and layout of one media fragment, for tests and
// the scenario generator that need to describe a fragment before writing it.
func Fragment(t TrackSpec, seq uint32, baseDecodeTime int64, samples []SampleSpec, seed int64, uri, init, rep string) ([]byte, SegmentLayout) {
	return fragment(t, seq, baseDecodeTime, samples, seed, uri, init, rep)
}

// Init returns the initialization segment bytes for a track.
func Init(t TrackSpec) []byte { return buildInit(t) }

// WriteFile writes data to dir/name, creating parents, and returns its size.
func WriteFile(dir, name string, data []byte) (int64, error) { return writeFile(dir, name, data) }
