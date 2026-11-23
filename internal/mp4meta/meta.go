// Package mp4meta builds and reads the small ISO-BMFF metadata documents that
// accompany archived representations. Only metadata boxes are written: no
// media payload is stored here, since the elementary stream remains the
// archival master. go-mp4 provides the box layout.
package mp4meta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/abema/go-mp4"
)

// Track is the metadata of one representation layer.
type Track struct {
	ID        uint32
	Timescale uint32
	// Times holds one absolute timestamp per coded frame, in Timescale ticks.
	Times []int64
	// Bytes holds one byte size per coded frame.
	Bytes []int64
	// Keyframes lists the frame indices that begin at a keyframe, 0-based.
	Keyframes []int
	Handler   [4]byte
}

// Document is a complete metadata archive for all layers.
type Document struct {
	Tracks []Track
}

// LayerMeta is the decoded metadata of one track.
type LayerMeta struct {
	TrackID        uint32
	Timescale      uint32
	Duration       uint64
	SampleCount    int
	TotalBytes     uint64
	KeyframeTimes  []int64 // ticks on Timescale
	KeyframeFrames []uint32
	Handler        [4]byte
}

// Encode writes ftyp plus a moov holding one video track per layer. The output
// is deterministic: no timestamps, no random identifiers.
func Encode(doc Document) ([]byte, error) {
	if len(doc.Tracks) == 0 {
		return nil, errors.New("mp4meta: no tracks")
	}
	var buf bytes.Buffer
	w := mp4.NewWriter(&seekWriter{buf: &buf})
	if err := writeFtyp(w); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := writeMvhd(w, doc); err != nil {
		return nil, err
	}
	for i, tr := range doc.Tracks {
		if err := writeTrack(w, tr, i); err != nil {
			return nil, fmt.Errorf("mp4meta: track %d: %w", i, err)
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	_, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   mp4.BrandISOM(),
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.BrandISOM()},
			{CompatibleBrand: mp4.BrandISO2()},
			{CompatibleBrand: mp4.BrandAVC1()},
		},
	}, mp4.Context{})
	if err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeMvhd(w *mp4.Writer, doc Document) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	ts, dur := 1000, uint64(0)
	for _, tr := range doc.Tracks {
		if d := uint64(lastTime(tr) * 1000 / int64(max32(tr.Timescale))); d > dur {
			dur = d
		}
	}
	_, err := mp4.Marshal(w, &mp4.Mvhd{
		Timescale: uint32(ts), DurationV0: uint32(min64(int64(dur), math.MaxUint32)),
		Rate: 0x10000, Volume: 0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: uint32(len(doc.Tracks) + 1),
	}, mp4.Context{})
	if err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeTrack(w *mp4.Writer, tr Track, index int) error {
	if tr.Timescale == 0 {
		return errors.New("zero timescale")
	}
	if len(tr.Times) == 0 {
		return errors.New("no samples")
	}
	if len(tr.Bytes) != len(tr.Times) {
		return fmt.Errorf("sample count mismatch: times=%d bytes=%d", len(tr.Times), len(tr.Bytes))
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := writeTkhd(w, tr, index); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := writeMdhd(w, tr); err != nil {
		return err
	}
	handler := tr.Handler
	if handler == [4]byte{} {
		handler = [4]byte{'v', 'i', 'd', 'e'}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Hdlr{HandlerType: handler, Name: "VideoHandler"}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeSamples(w, tr); err != nil {
		return err
	}
	for _, t := range []mp4.BoxType{mp4.BoxTypeStbl(), mp4.BoxTypeMinf(), mp4.BoxTypeMdia(), mp4.BoxTypeTrak()} {
		if _, err := w.EndBox(); err != nil {
			_ = t
			return err
		}
	}
	return nil
}

func writeTkhd(w *mp4.Writer, tr Track, index int) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	dur := lastTime(tr)
	_, err := mp4.Marshal(w, &mp4.Tkhd{
		TrackID: uint32(index + 1), DurationV0: uint32(min64(dur, math.MaxUint32)),
		Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		Width:  1920 << 16, Height: 1080 << 16,
	}, mp4.Context{})
	if err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeMdhd(w *mp4.Writer, tr Track) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	_, err := mp4.Marshal(w, &mp4.Mdhd{
		Timescale: tr.Timescale, DurationV0: uint32(min64(lastTime(tr), math.MaxUint32)),
		Language: [3]byte{'u', 'n', 'd'},
	}, mp4.Context{})
	if err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeSamples(w *mp4.Writer, tr Track) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	// stts encodes the distance from one sample to the next, so sample i's
	// presentation time is the sum of the first i deltas. Equal neighbours are
	// collapsed into runs. A regressing time is rejected outright: metadata is
	// a monotonic audit artifact.
	var deltas []int64
	for i := 0; i+1 < len(tr.Times); i++ {
		d := tr.Times[i+1] - tr.Times[i]
		if d < 0 {
			return fmt.Errorf("sample %d time regresses: %d -> %d", i+1, tr.Times[i], tr.Times[i+1])
		}
		deltas = append(deltas, d)
	}
	// stts needs exactly one delta per sample. Only n-1 intervals are real; a
	// sample's start time is unaffected by its own delta, so the final entry
	// repeats the last real interval.
	if len(deltas) == 0 {
		deltas = []int64{0}
	} else {
		deltas = append(deltas, deltas[len(deltas)-1])
	}
	entries := []mp4.SttsEntry{}
	var runCount uint32
	var runDelta int64 = -1
	for _, d := range deltas {
		if d == runDelta {
			runCount++
			continue
		}
		if runCount > 0 {
			entries = append(entries, mp4.SttsEntry{SampleCount: runCount, SampleDelta: uint32(runDelta)})
		}
		runDelta = d
		runCount = 1
	}
	if runCount > 0 {
		entries = append(entries, mp4.SttsEntry{SampleCount: runCount, SampleDelta: uint32(runDelta)})
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(entries)), Entries: entries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
		return err
	}
	sync := make([]uint32, 0, len(tr.Keyframes))
	for _, k := range tr.Keyframes {
		if k < 0 || k >= len(tr.Times) {
			return fmt.Errorf("keyframe index %d outside %d samples", k, len(tr.Times))
		}
		sync = append(sync, uint32(k+1)) // stss is 1-based
	}
	if _, err := mp4.Marshal(w, &mp4.Stss{EntryCount: uint32(len(sync)), SampleNumber: sync}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	sizes := make([]uint32, len(tr.Bytes))
	for i, b := range tr.Bytes {
		if b < 0 || b > math.MaxUint32 {
			return fmt.Errorf("sample %d size %d out of range", i, b)
		}
		sizes[i] = uint32(b)
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// seekWriter adapts a bytes.Buffer to the io.WriteSeeker the mp4 writer needs.
type seekWriter struct {
	buf *bytes.Buffer
	pos int64
}

func (s *seekWriter) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(s.buf.Len()) {
		extra := end - int64(s.buf.Len())
		s.buf.Write(make([]byte, extra))
	}
	copy(s.buf.Bytes()[s.pos:], p)
	s.pos = end
	return len(p), nil
}

func (s *seekWriter) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = int64(s.buf.Len()) + off
	default:
		return 0, fmt.Errorf("mp4meta: invalid whence %d", whence)
	}
	if s.pos < 0 {
		return 0, fmt.Errorf("mp4meta: negative seek %d", s.pos)
	}
	return s.pos, nil
}

func lastTime(tr Track) int64 {
	if len(tr.Times) == 0 {
		return 0
	}
	return tr.Times[len(tr.Times)-1]
}

func max32(v uint32) int64 {
	if v == 0 {
		return 1
	}
	return int64(v)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
