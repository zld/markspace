// Package mp4adapt is the MP4 acceptance adapter. It renders the manifest's
// segment time base into a minimal valid MP4 container (ftyp + moov with
// movie and media header timescales) and reads it back through go-mp4 to
// prove that the MP4 sample time base agrees with the canonical nanosecond
// manifest timestamps within one nanosecond.
package mp4adapt

import (
	"bytes"
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"

	"example.com/continuity/internal/clock"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/manifest"
)

// TimelineFile is the rendered MP4 artifact name inside the run directory.
const TimelineFile = "timeline.mp4"

// timescale is the MP4 movie/media ticks-per-second used for the adapter.
const timescale uint32 = 90000

// Render builds a minimal MP4 whose mvhd/mdhd duration encodes the manifest
// timeline. Only header boxes are required; sample payloads are not needed
// for time-base acceptance.
func Render(segments []manifest.SegmentRecord) ([]byte, error) {
	if len(segments) == 0 {
		return nil, fault.New(fault.StageManifest, "mp4-render", errors.New("no segments"))
	}
	last := segments[len(segments)-1]
	endNS := last.EndNS
	ticks, err := clock.FromNanos(endNS, int64(timescale))
	if err != nil {
		return nil, fault.New(fault.StageManifest, "mp4-ticks", err)
	}

	mem := newMemWriteSeeker()
	w := mp4.NewWriter(mem)

	// ftyp
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	// moov > mvhd
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := writeMvhd(w, uint64(ticks)); err != nil {
		return nil, err
	}
	// moov > trak > mdia > mdhd
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return nil, err
	}
	if err := writeMdhd(w, uint64(ticks)); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, err
	}
	return mem.Bytes(), nil
}

// memWriteSeeker is an in-memory io.WriteSeeker. Writes at an earlier offset
// (go-mp4 back-patches box sizes) overwrite bytes; seeks past the end grow
// the buffer with zeros.
type memWriteSeeker struct {
	buf []byte
	pos int64
}

func newMemWriteSeeker() *memWriteSeeker { return &memWriteSeeker{} }

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = m.pos + offset
	case io.SeekEnd:
		target = int64(len(m.buf)) + offset
	}
	if target < 0 {
		return 0, errors.New("negative seek")
	}
	m.pos = target
	return target, nil
}

func (m *memWriteSeeker) Bytes() []byte { return m.buf }

func writeMvhd(w *mp4.Writer, duration uint64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{
		Timescale:   timescale,
		DurationV0:  uint32(duration),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: 2,
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeMdhd(w *mp4.Writer, duration uint64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale:  timescale,
		DurationV0: uint32(duration),
		Language:   [3]byte{'u', 'n', 'd'},
	}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

// Timeline is the time base read back from an MP4.
type Timeline struct {
	Timescale  uint32
	DurationNS int64
}

// ReadBack parses an MP4 and returns its movie timescale converted to
// canonical nanoseconds via the shared clock package.
func ReadBack(data []byte) (*Timeline, error) {
	r := bytes.NewReader(data)
	var got *mp4.Mvhd
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type == mp4.BoxTypeMvhd() {
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if mv, ok := box.(*mp4.Mvhd); ok {
				got = mv
			}
		}
		return h.Expand()
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fault.New(fault.StageManifest, "mp4-read", err)
	}
	if got == nil {
		return nil, fault.New(fault.StageManifest, "mp4-read", errors.New("mvhd box not found"))
	}
	durTicks := int64(got.GetDuration())
	ns, err := clock.ToNanos(durTicks, int64(got.Timescale))
	if err != nil {
		return nil, fault.New(fault.StageManifest, "mp4-convert", err)
	}
	return &Timeline{Timescale: got.Timescale, DurationNS: ns}, nil
}

// AlignmentErrorNS returns the absolute difference between the manifest's
// last segment end and the MP4 movie duration, both in nanoseconds.
func AlignmentErrorNS(segments []manifest.SegmentRecord, tl *Timeline) (int64, error) {
	if len(segments) == 0 {
		return 0, errors.New("empty segments")
	}
	want := segments[len(segments)-1].EndNS
	diff := tl.DurationNS - want
	if diff < 0 {
		diff = -diff
	}
	return diff, nil
}
