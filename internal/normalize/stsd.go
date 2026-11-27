// Package normalize stsd sample-entry construction.
package normalize

import (
	mp4 "github.com/abema/go-mp4"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

func sampleEntryType(codec string) (mp4.BoxType, error) {
	switch codec {
	case ingest.CodecH264:
		return mp4.BoxTypeAvc1(), nil
	case ingest.CodecH265:
		return mp4.BoxTypeHev1(), nil
	case ingest.CodecAV1:
		return mp4.BoxTypeAv01(), nil
	default:
		return mp4.BoxType{}, model.New(model.ErrUnknownCodec, codec, -1, "no sample entry for codec")
	}
}

func configBoxType(codec string) (mp4.BoxType, error) {
	switch codec {
	case ingest.CodecH264:
		return mp4.StrToBoxType("avcC"), nil
	case ingest.CodecH265:
		return mp4.StrToBoxType("hvcC"), nil
	case ingest.CodecAV1:
		return mp4.BoxTypeAv1C(), nil
	default:
		return mp4.BoxType{}, model.New(model.ErrUnknownCodec, codec, -1, "no config box for codec")
	}
}

func (s *mp4Writer) writeStsd(t *Track, codecConfig []byte, ctx mp4.Context) error {
	entryType, err := sampleEntryType(t.Codec)
	if err != nil {
		return err
	}
	cfgType, err := configBoxType(t.Codec)
	if err != nil {
		return err
	}
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Stsd{EntryCount: 1}, ctx); err != nil {
		return err
	}

	// Visual sample entry (avc1/hev1/av01).
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: entryType}); err != nil {
		return err
	}
	entry := &mp4.VisualSampleEntry{}
	entry.SetType(entryType)
	entry.DataReferenceIndex = 1
	entry.Width = t.Width
	entry.Height = t.Height
	entry.Horizresolution = 0x00480000 // 72 dpi
	entry.Vertresolution = 0x00480000
	entry.FrameCount = 1
	entry.Depth = 0x0018
	entry.PreDefined3 = -1
	if _, err := mp4.Marshal(s.mw, entry, ctx); err != nil {
		return err
	}

	// Codec-specific config as an opaque child box: go-mp4 frames the header
	// and fixes up the size; the validated config bytes are written verbatim.
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: cfgType}); err != nil {
		return err
	}
	if _, err := s.mw.Write(codecConfig); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil { // config
		return err
	}
	if _, err := s.mw.EndBox(); err != nil { // sample entry
		return err
	}
	_, err = s.mw.EndBox() // stsd
	return err
}
