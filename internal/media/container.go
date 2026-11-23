// Package media synthesizes and probes ISO-BMFF (MP4) container metadata
// with abema/go-mp4. The pipeline works on locally generated samples, so a
// minimal valid ftyp/moov container is built in-memory and its movie header
// is probed back to establish the container time base that the manifest
// records alongside the nanosecond tick scale.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediaverify/internal/errs"
)

// memFile is an in-memory io.WriteSeeker; the MP4 writer seeks backwards to
// patch box sizes, which bytes.Buffer cannot do.
type memFile struct {
	data []byte
	pos  int64
}

func (m *memFile) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if int64(len(m.data)) < end {
		grown := make([]byte, end)
		copy(grown, m.data)
		m.data = grown
	}
	copy(m.data[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = m.pos + offset
	case io.SeekEnd:
		target = int64(len(m.data)) + offset
	default:
		return 0, errs.New(errs.KindInternal, "media", "bad seek whence %d", whence)
	}
	if target < 0 {
		return 0, errs.New(errs.KindInternal, "media", "negative seek")
	}
	m.pos = target
	return target, nil
}

// ContainerSummary is the probed MP4 metadata used by the manifest.
type ContainerSummary struct {
	Timescale uint32 // mvhd timescale (units per second)
	Duration  uint64 // mvhd duration in timescale units
	Brand     string // major brand from ftyp
	SHA256    string // digest of the serialized container bytes
}

// Synthesize writes a minimal valid MP4: an ftyp box and a moov/mvhd movie
// header carrying the given timescale and duration.
func Synthesize(timescale uint32, durationUnits uint64) ([]byte, error) {
	if timescale == 0 {
		return nil, errs.New(errs.KindInvalidInput, "media", "timescale must be non-zero")
	}
	var buf memFile
	w := mp4.NewWriter(&buf)

	ftypBI, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()})
	if err != nil {
		return nil, errs.New(errs.KindInternal, "media", "ftyp box: %v", err)
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return nil, errs.New(errs.KindInternal, "media", "marshal ftyp: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.KindInternal, "media", "end ftyp: %v", err)
	}

	moovBI, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()})
	if err != nil {
		return nil, errs.New(errs.KindInternal, "media", "moov box: %v", err)
	}
	mvhdBI, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()})
	if err != nil {
		return nil, errs.New(errs.KindInternal, "media", "mvhd box: %v", err)
	}
	mvhd := &mp4.Mvhd{
		Timescale:   timescale,
		DurationV0:  uint32(durationUnits),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: 1,
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return nil, errs.New(errs.KindInternal, "media", "marshal mvhd: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.KindInternal, "media", "end mvhd: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.KindInternal, "media", "end moov: %v", err)
	}
	_ = ftypBI
	_ = moovBI
	_ = mvhdBI
	return buf.data, nil
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

// Probe reads back the movie header and brand from serialized MP4 bytes.
func Probe(raw []byte) (ContainerSummary, error) {
	r := bytes.NewReader(raw)
	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeFtyp()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()},
	})
	if err != nil {
		return ContainerSummary{}, errs.New(errs.KindInvalidInput, "media", "extract boxes: %v", err)
	}
	sum := ContainerSummary{}
	foundMvhd := false
	for _, b := range boxes {
		switch v := b.Payload.(type) {
		case *mp4.Ftyp:
			sum.Brand = string(v.MajorBrand[:])
		case *mp4.Mvhd:
			sum.Timescale = v.Timescale
			sum.Duration = uint64(v.DurationV0)
			foundMvhd = true
		}
	}
	if !foundMvhd {
		return ContainerSummary{}, errs.New(errs.KindInvalidInput, "media", "moov/mvhd box missing")
	}
	digest := sha256.Sum256(raw)
	sum.SHA256 = hex.EncodeToString(digest[:])
	return sum, nil
}
