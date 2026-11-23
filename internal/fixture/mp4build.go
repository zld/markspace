package fixture

import (
	"github.com/abema/go-mp4"
)

// seekableBuffer supports writing at an arbitrary offset so that mp4.Writer
// can seek back and patch the box length headers when EndBox is called.
type seekableBuffer struct {
	buf []byte
	pos int64
}

func (s *seekableBuffer) Write(p []byte) (int, error) {
	if end := s.pos + int64(len(p)); end > int64(len(s.buf)) {
		s.buf = append(s.buf, make([]byte, end-int64(len(s.buf)))...)
	}
	copy(s.buf[s.pos:], p)
	s.pos += int64(len(p))
	return len(p), nil
}

func (s *seekableBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		s.pos = offset
	case 1:
		s.pos += offset
	case 2:
		s.pos = int64(len(s.buf)) + offset
	}
	return s.pos, nil
}

// Bytes returns the accumulated content.
func (s *seekableBuffer) Bytes() []byte { return s.buf }

// BuildInit serializes a valid fMP4 initialization segment: ftyp + moov
// (mvhd/trak/mdia/mdhd/minf/stbl/stsd/stsz) carrying an mcid metadata box.
func BuildInit(r *Representation) ([]byte, error) {
	buf := &seekableBuffer{}
	w := mp4.NewWriter(buf)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       brand("iso5"),
		MinorVersion:     1,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: brand("iso5")}, {CompatibleBrand: brand("dash")}},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mvhd{Timescale: 1000, DurationV0: uint32(nominalSegmentCount) * 400},
		mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{Timescale: uint32(r.Timescale), DurationV0: uint32(r.SegmentDuration) * nominalSegmentCount},
		mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mcid")}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, metaBox(r), mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, err
	}
	return buf.Bytes(), nil
}

// BuildMedia serializes one fMP4 media segment: optional sidx + styp +
// moof(mfhd/traf(tfhd/tfdt/trun)) + mdat, plus an mcid hint box.
func BuildMedia(r *Representation, seg *Segment) ([]byte, error) {
	buf := &seekableBuffer{}
	w := mp4.NewWriter(buf)
	if seg.IndexPresent {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSidx()}); err != nil {
			return nil, err
		}
		if _, err := mp4.Marshal(w, &mp4.Sidx{
			ReferenceID: 1, Timescale: uint32(r.Timescale),
			EarliestPresentationTimeV0: uint32(seg.BaseDecode),
			FirstOffsetV0:              0, ReferenceCount: 1,
			References: []mp4.SidxReference{{
				ReferencedSize: uint32(len(seg.Data)), SubsegmentDuration: uint32(seg.Duration),
				StartsWithSAP: true, SAPType: 2,
			}},
		}, mp4.Context{}); err != nil {
			return nil, err
		}
		if _, err := w.EndBox(); err != nil {
			return nil, err
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Styp{
		MajorBrand:       brand("msdh"),
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: brand("msdh")}},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMfhd()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mfhd{SequenceNumber: seg.Sequence}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfhd()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Tfhd{TrackID: 1}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfdt()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Tfdt{BaseMediaDecodeTimeV0: uint32(seg.BaseDecode)},
		mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mcid")}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, segBox(r, seg), mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // moof
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdat{Data: seg.Data}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func metaBox(r *Representation) *MCID {
	return &MCID{
		Timescale:      uint32(r.Timescale),
		TrackChannels:  uint16(r.Meta.Channels),
		SampleRate:     uint32(r.Meta.SampleRate),
		ColorPrimaries: r.Meta.ColorPrimaries,
		TransferChar:   r.Meta.TransferChar,
		MatrixCoeffs:   r.Meta.MatrixCoeffs,
	}
}

func segBox(r *Representation, seg *Segment) *MCID {
	b := metaBox(r)
	b.SegmentSeq = seg.Sequence
	b.BaseDecodeTime = uint64(seg.BaseDecode)
	if seg.IndexPresent {
		b.IndexPresent = 1
	}
	return b
}
