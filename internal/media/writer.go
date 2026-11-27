// Package media: writing the released PCM container.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/abema/go-mp4"

	"example.com/assetpack/internal/audio"
	"example.com/assetpack/internal/fault"
)

// Write encodes a PCM buffer as a self-contained MP4 and returns both the
// bytes and the index read back out of those bytes.
func Write(p audio.PCM, creation uint32) ([]byte, Index, error) {
	if err := p.Validate(); err != nil {
		return nil, Index{}, err
	}
	frames := p.Frames()
	if frames == 0 {
		return nil, Index{}, fault.New(fault.StageAudio, fault.KindInvalidArg, "refusing to write an empty container")
	}
	data := interleave(p)
	layout := buildLayout(p.Frames(), len(p.Channels))

	ws := &memWriteSeeker{}
	w := mp4.NewWriter(ws)
	ctx := mp4.Context{}

	if err := start(w, mp4.BoxTypeFtyp()); err != nil {
		return nil, Index{}, err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   mp4.BrandISOM(),
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.BrandISOM()},
			{CompatibleBrand: mp4.BrandISO2()},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, ctx); err != nil {
		return nil, Index{}, wrap("ftyp", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, Index{}, wrap("ftyp", err)
	}

	if err := start(w, mp4.BoxTypeMoov()); err != nil {
		return nil, Index{}, err
	}
	if err := writeMvhd(w, ctx, uint64(frames), p.Format.Rate, creation); err != nil {
		return nil, Index{}, err
	}
	if err := start(w, mp4.BoxTypeTrak()); err != nil {
		return nil, Index{}, err
	}
	if err := writeTkhd(w, ctx, uint64(frames), creation); err != nil {
		return nil, Index{}, err
	}
	if err := start(w, mp4.BoxTypeMdia()); err != nil {
		return nil, Index{}, err
	}
	if err := writeMdhd(w, ctx, uint64(frames), p.Format.Rate, creation); err != nil {
		return nil, Index{}, err
	}
	if err := writeHdlr(w, ctx); err != nil {
		return nil, Index{}, err
	}
	if err := start(w, mp4.BoxTypeMinf()); err != nil {
		return nil, Index{}, err
	}
	if err := writeSmhd(w, ctx); err != nil {
		return nil, Index{}, err
	}
	if err := writeDinf(w, ctx); err != nil {
		return nil, Index{}, err
	}
	if err := start(w, mp4.BoxTypeStbl()); err != nil {
		return nil, Index{}, err
	}
	if err := writeStbl(w, ctx, layout, p); err != nil {
		return nil, Index{}, err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return nil, Index{}, wrap("stbl", err)
	}
	if _, err := w.EndBox(); err != nil { // minf
		return nil, Index{}, wrap("minf", err)
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return nil, Index{}, wrap("mdia", err)
	}
	if _, err := w.EndBox(); err != nil { // trak
		return nil, Index{}, wrap("trak", err)
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, Index{}, wrap("moov", err)
	}
	// The single chunk begins at the mdat payload, which starts eight bytes
	// (the mdat header) after the current write position.
	if err := patchStco(ws.buf, uint32(ws.pos+8)); err != nil {
		return nil, Index{}, err
	}

	if err := start(w, mp4.BoxTypeMdat()); err != nil {
		return nil, Index{}, err
	}
	mdat := &mp4.Mdat{Data: data}
	if _, err := mp4.Marshal(w, mdat, ctx); err != nil {
		return nil, Index{}, wrap("mdat", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, Index{}, wrap("mdat", err)
	}

	out := append([]byte(nil), ws.buf...)
	ix, err := ReadIndex(bytes.NewReader(out))
	if err != nil {
		return nil, Index{}, err
	}
	ix.Frames = int64(frames)
	ix.Size = int64(len(out))
	sum := sha256.Sum256(out)
	ix.Digest = hex.EncodeToString(sum[:])
	if err := ix.Validate(int64(frames), p.Format.Rate, out); err != nil {
		return nil, Index{}, err
	}
	return out, ix, nil
}

func patchStco(buf []byte, offset uint32) error {
	idx := bytes.Index(buf, []byte("stco"))
	if idx < 0 {
		return fault.New(fault.StageAudio, fault.KindMissing, "stco box not found in container")
	}
	// stco payload: version/flags (4) + entry_count (4) + first offset (4).
	pos := idx + 4 + 4 + 4
	if pos+4 > len(buf) {
		return fault.New(fault.StageAudio, fault.KindTruncated, "stco box truncated")
	}
	binary.BigEndian.PutUint32(buf[pos:], offset)
	return nil
}
