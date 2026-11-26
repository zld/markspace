// Package fixture builds deterministic local media used by tests and by the
// regress fixture command. Nothing here reads external assets.
package fixture

import (
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// SampleSpec is one sample to place in a fragment.
type SampleSpec struct {
	Duration uint32
	Size     uint32
	Keyframe bool
	Payload  []byte
}

// TrackSpec describes an audio or video track for an init segment.
type TrackSpec struct {
	TrackID    uint32
	Timescale  uint32
	Handler    string // "soun" or "vide"
	Codec      string // "ipcm" or "mp4a" or "avc1"
	Channels   int
	SampleSize int
	SampleRate int
	Width      int
	Height     int
	Language   [3]byte
	// SampleEntryType overrides the stsd sample entry fourcc. When set to
	// "enca" the audio entry is wrapped, which a remuxer must reject.
	SampleEntryType string
}

// FragmentSpec is one moof+mdat to append.
type FragmentSpec struct {
	Sequence       uint32
	BaseDecodeTime uint64
	TrackID        uint32
	Samples        []SampleSpec
}

func fullBox(box *mp4.FullBox) { box.Version = 0 }

func writeBox(w *mp4.Writer, typ mp4.BoxType, payload mp4.IImmutableBox, ctx mp4.Context) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, payload, ctx); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// BuildInit writes an fMP4 initialisation segment with one trex per track.
func BuildInit(tracks []TrackSpec) ([]byte, error) {
	buf := &posWriter{}
	w := mp4.NewWriter(buf)
	ftyp := &mp4.Ftyp{
		MajorBrand:   mp4.BrandISOM(),
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.BrandISO6()},
			{CompatibleBrand: [4]byte{'d', 'a', 's', 'h'}},
			{CompatibleBrand: [4]byte{'i', 'p', 'c', 'm'}},
		},
	}
	if err := writeBox(w, mp4.BoxTypeFtyp(), ftyp, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	mvhd := &mp4.Mvhd{Timescale: 1000, Rate: 0x00010000, Volume: 0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: uint32(len(tracks) + 1)}
	fullBox(&mvhd.FullBox)
	if err := writeBox(w, mp4.BoxTypeMvhd(), mvhd, mp4.Context{}); err != nil {
		return nil, err
	}
	for _, t := range tracks {
		if err := writeTrak(w, t); err != nil {
			return nil, err
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvex()}); err != nil {
		return nil, err
	}
	for _, t := range tracks {
		trex := &mp4.Trex{TrackID: t.TrackID, DefaultSampleDescriptionIndex: 1}
		fullBox(&trex.FullBox)
		if err := writeBox(w, mp4.BoxTypeTrex(), trex, mp4.Context{}); err != nil {
			return nil, err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return buf.buf, nil
}

func writeTrak(w *mp4.Writer, t TrackSpec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{TrackID: t.TrackID, Volume: 0x0100,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}}
	fullBox(&tkhd.FullBox)
	tkhd.SetFlags(0x000007)
	if err := writeBox(w, mp4.BoxTypeTkhd(), tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	lang := t.Language
	if lang == [3]byte{} {
		lang = [3]byte{'u', 'n', 'd'}
	}
	mdhd := &mp4.Mdhd{Timescale: t.Timescale, Language: lang}
	fullBox(&mdhd.FullBox)
	if err := writeBox(w, mp4.BoxTypeMdhd(), mdhd, mp4.Context{}); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{t.Handler[0], t.Handler[1], t.Handler[2], t.Handler[3]}, Name: "Fixture"}
	fullBox(&hdlr.FullBox)
	if err := writeBox(w, mp4.BoxTypeHdlr(), hdlr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if t.Handler == "vide" {
		vmhd := &mp4.Vmhd{}
		fullBox(&vmhd.FullBox)
		if err := writeBox(w, mp4.BoxTypeVmhd(), vmhd, mp4.Context{}); err != nil {
			return err
		}
	} else {
		smhd := &mp4.Smhd{}
		fullBox(&smhd.FullBox)
		if err := writeBox(w, mp4.BoxTypeSmhd(), smhd, mp4.Context{}); err != nil {
			return err
		}
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, t); err != nil {
		return err
	}
	stts := &mp4.Stts{}
	fullBox(&stts.FullBox)
	if err := writeBox(w, mp4.BoxTypeStts(), stts, mp4.Context{}); err != nil {
		return err
	}
	stsc := &mp4.Stsc{}
	fullBox(&stsc.FullBox)
	if err := writeBox(w, mp4.BoxTypeStsc(), stsc, mp4.Context{}); err != nil {
		return err
	}
	stsz := &mp4.Stsz{}
	fullBox(&stsz.FullBox)
	if err := writeBox(w, mp4.BoxTypeStsz(), stsz, mp4.Context{}); err != nil {
		return err
	}
	stco := &mp4.Stco{}
	fullBox(&stco.FullBox)
	if err := writeBox(w, mp4.BoxTypeStco(), stco, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.EndBox() // trak
	return err
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	dref := &mp4.Dref{EntryCount: 1}
	fullBox(&dref.FullBox)
	if _, err := mp4.Marshal(w, dref, mp4.Context{}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.FullBox.Flags = [3]byte{0, 0, 1}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, url, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeStsd(w *mp4.Writer, t TrackSpec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	stsd := &mp4.Stsd{EntryCount: 1}
	fullBox(&stsd.FullBox)
	if _, err := mp4.Marshal(w, stsd, mp4.Context{}); err != nil {
		return err
	}
	if t.Handler == "vide" {
		entry := &mp4.VisualSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType(t.Codec)},
				DataReferenceIndex: 1,
			},
			Width: uint16(t.Width), Height: uint16(t.Height), FrameCount: 1,
			Horizresolution: 0x00480000, Vertresolution: 0x00480000, Depth: 0x0018,
		}
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(t.Codec)}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
			return err
		}
		avcc := &mp4.AVCDecoderConfiguration{
			AnyTypeBox:           mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
			ConfigurationVersion: 1, Level: 0x1f,
			LengthSizeMinusOne: 3, NumOfSequenceParameterSets: 0, NumOfPictureParameterSets: 0}
		if err := writeBox(w, mp4.BoxTypeAvcC(), avcc, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	} else {
		entry := &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType(t.Codec)},
				DataReferenceIndex: 1,
			},
			ChannelCount: uint16(t.Channels),
			SampleSize:   uint16(t.SampleSize),
			SampleRate:   uint32(t.SampleRate) << 16,
		}
		entryType := t.Codec
		if t.SampleEntryType != "" {
			entryType = t.SampleEntryType
			entry.AnyTypeBox.Type = mp4.StrToBoxType(entryType)
		}
		if entryType == "enca" {
			if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeEnca()}); err != nil {
				return err
			}
			if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
				return err
			}
			if _, err := w.EndBox(); err != nil {
				return err
			}
		} else if t.Codec != "ipcm" {
			if err := writeBox(w, mp4.StrToBoxType(t.Codec), entry, mp4.Context{}); err != nil {
				return err
			}
		} else {
			// ipcm carries pcmC describing the stored sample size.
			if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeIpcm()}); err != nil {
				return err
			}
			if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
				return err
			}
			pcmC := &mp4.PcmC{FormatFlags: 0, PCMSampleSize: uint8(t.SampleSize)}
			fullBox(&pcmC.FullBox)
			if err := writeBox(w, mp4.BoxTypePcmC(), pcmC, mp4.Context{}); err != nil {
				return err
			}
			if _, err := w.EndBox(); err != nil {
				return err
			}
		}
	}
	if _, err := w.EndBox(); err != nil { // stsd
		return err
	}
	return nil
}

// SampleLoc is the absolute byte location of one generated sample.
type SampleLoc struct {
	Offset     int64
	Size       uint32
	Keyframe   bool
	DecodeTime uint64
	Duration   uint32
}

// SegmentLayout lists every sample of a generated segment by absolute offset.
type SegmentLayout struct {
	Samples []SampleLoc
}

// BuildSegment writes a styp + one or more moof/mdat pairs.
func BuildSegment(fragments []FragmentSpec) ([]byte, error) {
	b, _, err := BuildSegmentLayout(fragments)
	return b, err
}

// BuildSegmentLayout is BuildSegment plus the absolute sample locations, so
// callers can write an accurate I-frame byte-range playlist.
func BuildSegmentLayout(fragments []FragmentSpec) ([]byte, *SegmentLayout, error) {
	buf := &posWriter{}
	w := mp4.NewWriter(buf)
	styp := &mp4.Styp{MajorBrand: [4]byte{'m', 's', 'd', 'h'}}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStyp()}); err != nil {
		return nil, nil, err
	}
	if _, err := mp4.Marshal(w, styp, mp4.Context{}); err != nil {
		return nil, nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, nil, err
	}
	layout := &SegmentLayout{}
	for _, fr := range fragments {
		locs, err := appendFragmentLayout(w, fr, int64(len(buf.buf)))
		if err != nil {
			return nil, nil, err
		}
		layout.Samples = append(layout.Samples, locs...)
	}
	return buf.buf, layout, nil
}

func appendFragment(w *mp4.Writer, fr FragmentSpec) error {
	_, err := appendFragmentLayout(w, fr, 0)
	return err
}

// appendFragmentLayout writes one moof/mdat pair starting at absolute offset
// base and returns the absolute location of every sample.
func appendFragmentLayout(w *mp4.Writer, fr FragmentSpec, base int64) ([]SampleLoc, error) {
	// data_offset is relative to the start of the enclosing moof and equals
	// the moof size plus the 8-byte mdat header. moof size does not depend on
	// the data_offset value, so it can be measured with a first pass.
	first := &posWriter{}
	if err := marshalMoof(mp4.NewWriter(first), fr, 0); err != nil {
		return nil, err
	}
	moofSize := len(first.buf)
	second := &posWriter{}
	if err := marshalMoof(mp4.NewWriter(second), fr, int32(moofSize+8)); err != nil {
		return nil, err
	}
	if len(second.buf) != moofSize {
		return nil, fmt.Errorf("moof size changed between passes: %d != %d", len(second.buf), moofSize)
	}
	if _, err := w.Write(second.buf); err != nil {
		return nil, err
	}
	payloadStart := base + int64(len(second.buf)) + 8
	var locs []SampleLoc
	decode := fr.BaseDecodeTime
	var cursor int64
	for _, smp := range fr.Samples {
		size := smp.Size
		if size == 0 {
			size = uint32(len(smp.Payload))
		}
		locs = append(locs, SampleLoc{Offset: payloadStart + cursor, Size: size,
			Keyframe: smp.Keyframe, DecodeTime: decode, Duration: smp.Duration})
		cursor += int64(size)
		decode += uint64(smp.Duration)
	}
	var payload []byte
	for _, s := range fr.Samples {
		payload = append(payload, s.Payload...)
		if len(s.Payload) < int(s.Size) {
			payload = append(payload, make([]byte, int(s.Size)-len(s.Payload))...)
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	if _, err := w.Write(payload); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return locs, nil
}

func marshalMoof(w *mp4.Writer, fr FragmentSpec, dataOffset int32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return err
	}
	mfhd := &mp4.Mfhd{SequenceNumber: fr.Sequence}
	fullBox(&mfhd.FullBox)
	if err := writeBox(w, mp4.BoxTypeMfhd(), mfhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return err
	}
	tfhd := &mp4.Tfhd{TrackID: fr.TrackID}
	tfhd.FullBox.Version = 0
	tfhd.SetFlags(mp4.TfhdDefaultBaseIsMoof | mp4.TfhdDurationIsEmpty)
	if err := writeBoxRaw(w, mp4.BoxTypeTfhd(), tfhd); err != nil {
		return err
	}
	tfdt := &mp4.Tfdt{}
	tfdt.Version = 1
	tfdt.BaseMediaDecodeTimeV1 = fr.BaseDecodeTime
	if err := writeBox(w, mp4.BoxTypeTfdt(), tfdt, mp4.Context{}); err != nil {
		return err
	}
	trun := &mp4.Trun{SampleCount: uint32(len(fr.Samples)), DataOffset: dataOffset}
	trun.Version = 0
	trun.SetFlags(0x000001 | 0x000100 | 0x000200 | 0x000400)
	for _, s := range fr.Samples {
		var flags uint32
		if !s.Keyframe {
			flags = 0x00010000
		}
		trun.Entries = append(trun.Entries, mp4.TrunEntry{
			SampleDuration: s.Duration, SampleSize: s.Size, SampleFlags: flags,
		})
	}
	if err := writeBox(w, mp4.BoxTypeTrun(), trun, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return err
	}
	_, err := w.EndBox() // moof
	return err
}

// writeBoxRaw marshals a box whose optional fields depend on flags, using the
// box's own flag bits (Marshal consults GetFlags through field tags).
func writeBoxRaw(w *mp4.Writer, typ mp4.BoxType, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

type posWriter struct {
	buf []byte
	pos int64
}

func (p *posWriter) Write(b []byte) (int, error) {
	need := int(p.pos) + len(b)
	if need > cap(p.buf) {
		nb := make([]byte, need)
		copy(nb, p.buf)
		p.buf = nb
	}
	if need > len(p.buf) {
		p.buf = p.buf[:need]
	}
	copy(p.buf[p.pos:], b)
	p.pos += int64(len(b))
	return len(b), nil
}

func (p *posWriter) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		p.pos = off
	case io.SeekCurrent:
		p.pos += off
	case io.SeekEnd:
		p.pos = int64(len(p.buf)) + off
	}
	if p.pos < 0 {
		return 0, fmt.Errorf("negative seek")
	}
	return p.pos, nil
}
