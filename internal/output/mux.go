// Package output writes the recovered, timeline-aligned media to a local MP4
// file using github.com/abema/go-mp4.
//
// The muxer is deliberately narrow: it supports the generated fixtures only,
// one audio track plus an optional video track, and never invents codec
// configs. Every timing value comes from the shared clock.Timeline rather
// than local accumulation, and all metadata (creation times, ordering,
// padding) is fixed, so the same inputs always produce the same bytes and
// therefore the same SHA-256.
package output

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediapipeline/internal/clock"
	mpierr "mediapipeline/internal/errors"
)

// MaxSamplesPerTrack bounds output size; larger tracks are rejected.
const MaxSamplesPerTrack = 100000

// Sample is one muxed media unit already positioned on the timeline.
type Sample struct {
	// Data is the sample bytes written verbatim into mdat.
	Data []byte
	// Ticks is the sample duration on the canonical timeline.
	Ticks uint32
}

// Track is one output track.
type Track struct {
	Kind clock.Kind
	// TickRate is the canonical timeline rate the Ticks are counted in.
	TickRate uint32
	// Timescale is this track's MP4 timescale.
	Timescale uint32
	Samples   []Sample
}

// Summary is the verification record for a written file.
type Summary struct {
	Path       string
	Size       int64
	SHA256     string
	AudioCount int
	VideoCount int
}

var identity = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

// Write renders the tracks to path and returns a deterministic summary. The
// file is created (or truncated) at exactly the caller-provided local path.
func Write(path string, audio, video *Track) (Summary, error) {
	if path == "" {
		return Summary{}, mpierr.New(mpierr.CatLimit, 0, "mux", "output path is empty")
	}
	if err := validate(audio); err != nil {
		return Summary{}, err
	}
	if video != nil {
		if err := validate(video); err != nil {
			return Summary{}, err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return Summary{}, mpierr.New(mpierr.CatUnknown, 0, "mux", "cannot create output").Wrap(err)
	}
	defer f.Close()

	w := mp4.NewWriter(f)
	if err := writeFtyp(w); err != nil {
		return Summary{}, err
	}
	chunkOffsets, mdatEnd, err := writeMdat(w, audio, video)
	if err != nil {
		return Summary{}, err
	}
	moovEnd, err := writeMoov(w, audio, video, chunkOffsets)
	if err != nil {
		return Summary{}, err
	}
	totalSize := mdatEnd
	if moovEnd > totalSize {
		totalSize = moovEnd
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Summary{}, mpierr.New(mpierr.CatUnknown, 0, "mux", "seek for hash").Wrap(err)
	}
	sum, err := hashFile(f)
	if err != nil {
		return Summary{}, err
	}
	sum.Path, sum.Size = path, totalSize
	return sum, nil
}

func validate(t *Track) error {
	if t == nil {
		return mpierr.New(mpierr.CatLimit, 0, "mux", "nil track")
	}
	if t.Timescale == 0 {
		return mpierr.New(mpierr.CatLimit, 0, "mux", "track timescale is zero")
	}
	if t.TickRate == 0 {
		return mpierr.New(mpierr.CatLimit, 0, "mux", "track tick rate is zero")
	}
	if len(t.Samples) == 0 {
		return mpierr.New(mpierr.CatLimit, 0, "mux", "track has no samples")
	}
	if len(t.Samples) > MaxSamplesPerTrack {
		return mpierr.New(mpierr.CatLimit, 0, "mux",
			fmt.Sprintf("sample count %d exceeds %d", len(t.Samples), MaxSamplesPerTrack))
	}
	return nil
}

func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	brand := func(s string) [4]byte { var b [4]byte; copy(b[:], s); return b }
	ftyp := &mp4.Ftyp{
		MajorBrand:   brand("iso4"),
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: brand("iso4")}, {CompatibleBrand: brand("mp41")},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return mpierr.New(mpierr.CatUnknown, 0, "mux", "marshal ftyp").Wrap(err)
	}
	_, err := w.EndBox()
	return err
}

func hashFile(r io.Reader) (Summary, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return Summary{}, mpierr.New(mpierr.CatUnknown, 0, "mux", "hashing output").Wrap(err)
	}
	return Summary{SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
