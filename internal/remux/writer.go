// Package remux re-muxes parsed media into a new MP4 container. The sample
// table is known upfront, so the header (ftyp+moov+mdat header) is written
// first and sample payloads are streamed afterwards, keeping memory bounded
// by the downstream queue rather than the file size.
package remux

import (
	"encoding/binary"
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediacheck/internal/adapter"
	"mediacheck/internal/clock"
)

// TrackSpec describes one output track. The full sample table must be
// known before WriteHeader.
type TrackSpec struct {
	ID        uint32
	Kind      adapter.Kind
	Timescale clock.Timescale
	Samples   []adapter.Sample
}

// Writer streams a remuxed MP4 to an io.Writer.
type Writer struct {
	w          io.Writer
	specs      []TrackSpec
	written    []int // samples written per track
	total      int64 // bytes written so far
	headerDone bool
}

func New(w io.Writer) *Writer { return &Writer{w: w} }

// BytesWritten returns the number of bytes written so far.
func (w *Writer) BytesWritten() int64 { return w.total }

// WriteHeader writes ftyp, moov and the mdat header. movieDurationNanos is
// the media duration used for the movie header.
func (w *Writer) WriteHeader(specs []TrackSpec, movieDurationNanos int64) error {
	w.specs = specs
	w.written = make([]int, len(specs))

	ftypPayload, err := leaf(&gomp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []gomp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	})
	if err != nil {
		return err
	}
	ftyp := container("ftyp", ftypPayload)
	var payload uint64
	for _, s := range specs {
		for _, smp := range s.Samples {
			payload += uint64(smp.Size)
		}
	}
	moovLen := func(offsets []uint64) (int, []byte, error) {
		b, err := buildMoov(specs, offsets, uint32(clock.Timescale(1000).FromNanos(movieDurationNanos)))
		return len(b), b, err
	}
	placeholder := make([]uint64, len(specs))
	n, _, err := moovLen(placeholder)
	if err != nil {
		return err
	}
	// Chunk data starts right after ftyp + moov + mdat header.
	dataStart := uint64(len(ftyp)) + uint64(n) + 8
	offsets := make([]uint64, len(specs))
	acc := dataStart
	for i, s := range specs {
		offsets[i] = acc
		for _, smp := range s.Samples {
			acc += uint64(smp.Size)
		}
	}
	m, moov, err := moovLen(offsets)
	if err != nil {
		return err
	}
	if m != n {
		return fmt.Errorf("remux: moov size changed between passes (%d != %d)", m, n)
	}
	if err := w.write(ftyp); err != nil {
		return err
	}
	if err := w.write(moov); err != nil {
		return err
	}
	var mdatHdr [8]byte
	binary.BigEndian.PutUint32(mdatHdr[0:4], uint32(8+payload))
	copy(mdatHdr[4:8], "mdat")
	if err := w.write(mdatHdr[:]); err != nil {
		return err
	}
	w.headerDone = true
	return nil
}

// WriteSample appends the next sample payload of track ti. Tracks are laid
// out one chunk per track in spec order, so samples of track ti are only
// accepted once tracks 0..ti-1 are complete.
func (w *Writer) WriteSample(ti int, data []byte) error {
	if !w.headerDone {
		return fmt.Errorf("remux: WriteSample before WriteHeader")
	}
	if ti < 0 || ti >= len(w.specs) {
		return fmt.Errorf("remux: track index %d out of range", ti)
	}
	for prev := 0; prev < ti; prev++ {
		if w.written[prev] != len(w.specs[prev].Samples) {
			return fmt.Errorf("remux: track %d written before track %d completed", ti, prev)
		}
	}
	idx := w.written[ti]
	if idx >= len(w.specs[ti].Samples) {
		return fmt.Errorf("remux: too many samples for track %d", ti)
	}
	if want := w.specs[ti].Samples[idx].Size; uint32(len(data)) != want {
		return fmt.Errorf("remux: sample %d of track %d has %d bytes, want %d", idx, ti, len(data), want)
	}
	if err := w.write(data); err != nil {
		return err
	}
	w.written[ti]++
	return nil
}

// Finalize verifies all samples were written and returns the total size.
func (w *Writer) Finalize() (int64, error) {
	for ti := range w.specs {
		if w.written[ti] != len(w.specs[ti].Samples) {
			return 0, fmt.Errorf("remux: track %d incomplete: %d/%d samples",
				ti, w.written[ti], len(w.specs[ti].Samples))
		}
	}
	return w.total, nil
}

func (w *Writer) write(p []byte) error {
	n, err := w.w.Write(p)
	w.total += int64(n)
	return err
}
