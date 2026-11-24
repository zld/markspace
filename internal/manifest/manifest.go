// Package manifest wraps delivery metadata in a minimal MP4 container using
// go-mp4. The audio payload stays in the .pcm sidecar; this manifest records
// machine-checkable provenance (parameters, counters, hashes) as boxes.
package manifest

import (
	"bytes"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// Metadata is the provenance record embedded in the manifest.
type Metadata struct {
	JobID      string
	SourceRate int
	TargetRate int
	Layout     string
	TargetLUFS float64
	Packets    uint64
	Gaps       int
	SamplesIn  int64
	SamplesOut int64
	SHA256     string
}

// WriteBoxes emits ftyp + moov(udta) + mdat boxes. The mdat box carries the
// JSON metadata; the function exists to exercise the pinned go-mp4 writer.
func WriteBoxes(w io.WriteSeeker, m Metadata, payload []byte) error {
	ww := mp4.NewWriter(w)

	// File-type box identifying the brand.
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'m', 'd', 'a', 'd'},
		MinorVersion: 1,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}
	if _, err := mp4.Marshal(ww, ftyp, mp4.Context{}); err != nil {
		return err
	}
	if _, err := ww.EndBox(); err != nil {
		return err
	}

	// Movie box holding a user-data box with the JSON provenance.
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUdta()}); err != nil {
		return err
	}
	metaJSON := encodeMetadata(m)
	if _, err := ww.Write(metaJSON); err != nil {
		return err
	}
	if _, err := ww.EndBox(); err != nil { // udta
		return err
	}
	if _, err := ww.EndBox(); err != nil { // moov
		return err
	}

	// Media data box carrying the PCM payload reference bytes.
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	if _, err := io.Copy(ww, bytes.NewReader(payload)); err != nil {
		return err
	}
	if _, err := ww.EndBox(); err != nil {
		return err
	}
	return nil
}
