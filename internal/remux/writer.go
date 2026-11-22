// Package remux rebuilds the output container: it writes the repaired
// packet stream into an MP4 structure via go-mp4 with a fresh
// random-access index (stss sync table + stco chunk offsets), commits
// through a temp file with atomic rename, and validates track mappings
// before any output file is created.
package remux

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediaqc/internal/ebml"
	"mediaqc/internal/timeline"
)

// mediaTimescale is the unified clock: one unit = one nanosecond.
const mediaTimescale = 1_000_000_000

// codecKinds is the allowlist of source codecs this tool remuxes
// without re-encoding, mapped to their media kind.
var codecKinds = map[string]string{
	"V_VP8":  "video",
	"V_VP9":  "video",
	"V_AV1":  "video",
	"A_OPUS": "audio",
	"A_AAC":  "audio",
}

// CodecKind returns the media kind for a supported codec.
func CodecKind(codec string) (string, bool) {
	k, ok := codecKinds[codec]
	return k, ok
}

// ValidateMapping checks an explicit source->target track mapping
// against the scanned tracks: every source track must be mapped, no
// two sources may share a target, and every codec must be supported.
// Failures are positioned faults; the caller must not create output.
func ValidateMapping(mapping map[uint64]uint64, tracks []ebml.TrackInfo) error {
	seen := map[uint64]uint64{}
	for _, t := range tracks {
		target, ok := mapping[t.Number]
		if !ok {
			return timeline.NewFault(timeline.ClassTrackMapping, 0, t.Number,
				"no mapping for source track %d", t.Number)
		}
		if prev, dup := seen[target]; dup {
			return timeline.NewFault(timeline.ClassTrackMapping, 0, t.Number,
				"target track %d used by both source %d and %d", target, prev, t.Number)
		}
		seen[target] = t.Number
		if _, ok := CodecKind(t.Codec); !ok {
			return timeline.NewFault(timeline.ClassCodec, 0, t.Number,
				"codec %q is not supported for remux without re-encoding", t.Codec)
		}
	}
	return nil
}

// Track is one output track's running sample tables.
type Track struct {
	ID        uint32
	Source    uint64
	Kind      string
	codec     string
	sizes     []uint32
	deltas    []uint32
	keyframes []uint32
	chunks    []uint64
	duration  uint64
}

// Writer streams samples into an MP4 container. Samples become
// single-sample chunks in mdat; moov with the full random-access index
// is written at Close. Nothing but the sample tables is held in memory.
type Writer struct {
	w       *mp4.Writer
	tracks  []*Track
	mdatEnd func() error
}

// NewWriter writes ftyp and opens the mdat box on ws.
func NewWriter(ws io.WriteSeeker) (*Writer, error) {
	w := &Writer{w: mp4.NewWriter(ws)}
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 1,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	if _, err := mp4.Marshal(w.w, ftyp, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	w.mdatEnd = func() error {
		_, err := w.w.EndBox()
		return err
	}
	return w, nil
}

// AddTrack registers an output track for a mapped source track.
func (w *Writer) AddTrack(source, target uint64, codec string) (*Track, error) {
	kind, ok := CodecKind(codec)
	if !ok {
		return nil, timeline.NewFault(timeline.ClassCodec, 0, source,
			"codec %q unsupported", codec)
	}
	t := &Track{ID: uint32(target), Source: source, Kind: kind, codec: codec}
	w.tracks = append(w.tracks, t)
	return t, nil
}

// WriteSample appends one sample as its own chunk and records its
// index rows. keyframe feeds the stss random-access table.
func (w *Writer) WriteSample(t *Track, data []byte, keyframe bool, durNs uint32) error {
	pos, err := w.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := w.w.Write(data); err != nil {
		return err
	}
	t.chunks = append(t.chunks, uint64(pos))
	t.sizes = append(t.sizes, uint32(len(data)))
	t.deltas = append(t.deltas, durNs)
	t.duration += uint64(durNs)
	if keyframe {
		t.keyframes = append(t.keyframes, uint32(len(t.sizes)))
	}
	return nil
}

// Close finishes mdat and writes moov with all sample tables.
func (w *Writer) Close() error {
	if err := w.mdatEnd(); err != nil {
		return err
	}
	return w.writeMoov()
}

func (w *Writer) box(typ mp4.BoxType, payload mp4.IImmutableBox) error {
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if payload != nil {
		if _, err := mp4.Marshal(w.w, payload, mp4.Context{}); err != nil {
			return err
		}
	}
	_, err := w.w.EndBox()
	return err
}

func (w *Writer) writeMoov() error {
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	var movieDur uint64
	for _, t := range w.tracks {
		if d := t.duration / 1_000_000; d > movieDur { // movie timescale: ms
			movieDur = d
		}
	}
	err := w.box(mp4.BoxTypeMvhd(), &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  uint32(movieDur),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: uint32(len(w.tracks) + 1),
	})
	if err != nil {
		return err
	}
	for _, t := range w.tracks {
		if err := w.writeTrak(t); err != nil {
			return err
		}
	}
	_, err = w.w.EndBox()
	return err
}
