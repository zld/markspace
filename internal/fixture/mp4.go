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
		avcc := &mp4.AVCDecoderConfiguration{ConfigurationVersion: 1, Level: 0x1f,
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
		if err := writeBox(w, mp4.StrToBoxType(t.Codec), entry, mp4.Context{}); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil { // stsd
		return err
	}
	return nil
}

// BuildSegment writes a styp + one or more moof/mdat pairs.
func BuildSegment(fragments []FragmentSpec) ([]byte, error) {
	buf := &posWriter{}
	w := mp4.NewWriter(buf)
	styp := &mp4.Styp{MajorBrand: [4]byte{'m', 's', 'd', 'h'}}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, styp, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	for _, fr := range fragments {
		if err := appendFragment(w, fr); err != nil {
			return nil, err
		}
	}
	return buf.buf, nil
}

func appendFragment(w *mp4.Writer, fr FragmentSpec) error {
	// Marshal the moof into a scratch buffer first so trun.data_offset can be
	// computed relative to the moof start.
	scratch := &posWriter{}
	sw := mp4.NewWriter(scratch)
	if err := marshalMoof(sw, fr, 0); err != nil {
		return err
	}
	moofSize := len(scratch.buf)
	mdatHeader := 8
	// data_offset is relative to the start of the enclosing moof.
	dataOffset := int32(moofSize + mdatHeader)
	scratch.buf, scratch.pos = nil, 0
	if err := marshalMoof(sw, fr, dataOffset); err != nil {
		return err
	}
	if len(scratch.buf) != moofSize {
		return fmt.Errorf("moof size changed between passes: %d != %d", len(scratch.buf), moofSize)
	}
	if _, err := w.Write(scratch.buf); err != nil {
		return err
	}
	var payload []byte
	for _, s := range fr.Samples {
		payload = append(payload, s.Payload...)
		if len(s.Payload) < int(s.Size) {
			payload = append(payload, make([]byte, int(s.Size)-len(s.Payload))...)
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
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
