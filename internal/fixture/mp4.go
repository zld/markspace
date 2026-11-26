// Package fixture generates the deterministic, self-contained media inputs
// used by tests and the demo: valid and deliberately corrupted MP4 files,
// RTP captures including timestamp wrap, and edit-decision lists.
package fixture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
)

// MP4Spec describes a single-track, single-chunk MP4 fixture.
type MP4Spec struct {
	Path       string
	Timescale  uint32
	SampleDurs []uint32 // one stts delta per sample, ticks each
	Payload    [][]byte // one payload per sample
}

// WriteMP4 builds a minimal but standards-shaped MP4: ftyp, moov with one
// trak carrying mdhd and a full sample table, then mdat holding the
// payloads contiguously. All samples live in one chunk.
func WriteMP4(spec MP4Spec) error {
	if spec.Timescale == 0 {
		return errors.New("fixture: timescale must be non-zero for valid output")
	}
	if len(spec.SampleDurs) != len(spec.Payload) {
		return errors.New("fixture: sample durations and payload count differ")
	}
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(spec.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	// ftyp
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// moov > trak > mdhd + minf > stbl sample tables
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	var totalDur uint32
	for _, d := range spec.SampleDurs {
		totalDur += d
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{
		Timescale:  spec.Timescale,
		DurationV0: totalDur,
		Language:   [3]byte{'u', 'n', 'd'},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdhd
		return err
	}
	if err := writeSampleTables(w, spec); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}

	// mdat with concatenated payloads; chunk offset is the payload start.
	mdatBi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		return err
	}
	for _, p := range spec.Payload {
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// Patch the stco chunk offset now that mdat payload position is known.
	return patchChunkOffset(f, mdatBi.Offset+mdatBi.HeaderSize)
}

// writeSampleTables emits the minimal stbl children the parser consumes.
func writeSampleTables(w *mp4.Writer, spec MP4Spec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}

	// stts: run-length compress identical trailing deltas.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	entries := compressDeltas(spec.SampleDurs)
	if _, err := mp4.Marshal(w, &mp4.Stts{
		EntryCount: uint32(len(entries)),
		Entries:    entries,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// stsc: every sample is in the single chunk.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(spec.Payload))}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// stsz with explicit per-sample sizes.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	sizes := make([]uint32, len(spec.Payload))
	for i, p := range spec.Payload {
		sizes[i] = uint32(len(p))
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{
		SampleCount: uint32(len(sizes)),
		EntrySize:   sizes,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// stco placeholder; patched after mdat is written.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stco{
		EntryCount:  1,
		ChunkOffset: []uint32{0},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stco
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	_, err := w.EndBox() // minf
	return err
}

func compressDeltas(durs []uint32) []mp4.SttsEntry {
	var out []mp4.SttsEntry
	for _, d := range durs {
		if n := len(out); n > 0 && out[n-1].SampleDelta == d {
			out[n-1].SampleCount++
			continue
		}
		out = append(out, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
	}
	return out
}

// patchChunkOffset rewrites the 4-byte placeholder inside the stco entry.
// The stco box payload layout is: version/flags(4) entry_count(4) offset(4).
func patchChunkOffset(f *os.File, offset uint64) error {
	if offset > 0xffffffff {
		return fmt.Errorf("fixture offset %d exceeds 32-bit stco range", offset)
	}
	// Locate the stco box by re-reading the structure from the start.
	var stcoAbs int64 = -1
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		if h.Path[len(h.Path)-1] == mp4.BoxTypeStco() {
			stcoAbs = int64(h.BoxInfo.Offset + h.BoxInfo.HeaderSize + 8)
		}
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		return err
	}
	if stcoAbs < 0 {
		return errors.New("fixture: stco box not found while patching offset")
	}
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(offset))
	_, err := f.WriteAt(buf[:], stcoAbs)
	return err
}
