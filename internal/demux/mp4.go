// Package demux parses MP4/MOV containers into per-track sample tables while
// preserving compressed sample bytes, timescales, timing and keyframe
// positions. All parsing failures are translated into the stable error
// vocabulary of internal/errors, including the byte offset of the offending
// box.
package demux

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	merr "mediacheck/internal/errors"
)

// Kind is the track handler kind.
type Kind string

// Supported handler kinds.
const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// Sample is one compressed sample. Payload keeps the original container
// bytes untouched; times are integer ticks on the track timescale.
type Sample struct {
	Index    int
	Offset   int64 // absolute byte offset inside the file
	Size     int
	DTS      int64 // decode timestamp in track ticks
	Duration int64 // duration in track ticks
	Sync     bool  // keyframe / sync sample
}

// Track is the parsed view of one trak box.
type Track struct {
	ID         uint32
	Kind       Kind
	Codec      string // sample-entry fourCC, e.g. "avc1" / "mp4a"
	Timescale  uint32
	Duration   int64 // mdhd duration in track ticks
	Width      uint16
	Height     uint16
	Channels   uint16
	SampleRate uint32
	Samples    []Sample
}

// StartTick returns the earliest sample DTS (always >= 0 for our fixtures).
func (t *Track) StartTick() int64 {
	if len(t.Samples) == 0 {
		return 0
	}
	return t.Samples[0].DTS
}

// EndTick returns the timestamp immediately after the final sample.
func (t *Track) EndTick() int64 {
	if len(t.Samples) == 0 {
		return t.Duration
	}
	last := t.Samples[len(t.Samples)-1]
	return last.DTS + last.Duration
}

// Container is a parsed file.
type Container struct {
	MovieTimescale uint32
	Tracks         []*Track
	fileSize       int64
}

// Parse reads an MP4/MOV byte stream. The reader must be seekable so sample
// payloads can be read on demand via ReadSample.
func Parse(r io.ReadSeeker) (*Container, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, merr.New(merr.CodeInvalidBox, "cannot size input").Wrap(err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// Phase 1: validate every top-level box header against the real file size
	// so truncated boxes produce a stable code plus the exact offset.
	if err := validateTopLevel(r, size); err != nil {
		return nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	c := &Container{fileSize: size}
	tb := newTableBuilder()
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		if err := tb.ingest(h); err != nil {
			return nil, err
		}
		if h.BoxInfo.Size >= h.BoxInfo.HeaderSize && containerBox(h.BoxInfo.Type) {
			return h.Expand()
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		if e, ok := merr.As(err); ok {
			return nil, e
		}
		return nil, merr.New(merr.CodeInvalidBox, "box structure parse failed").
			WithState(merr.StateFatal).Wrap(err)
	}
	tracks, err := tb.finalize(size)
	if err != nil {
		return nil, err
	}
	c.MovieTimescale = tb.movieTimescale
	c.Tracks = tracks
	return c, nil
}

// ReadSample returns an independent copy of the compressed sample bytes.
func (c *Container) ReadSample(r io.ReaderAt, tr *Track, idx int) ([]byte, error) {
	if idx < 0 || idx >= len(tr.Samples) {
		return nil, merr.New(merr.CodeSampleOutOfRange,
			fmt.Sprintf("sample index %d out of range (track %d)", idx, tr.ID))
	}
	s := tr.Samples[idx]
	if s.Offset < 0 || s.Offset+int64(s.Size) > c.fileSize {
		return nil, merr.New(merr.CodeSampleOutOfRange,
			fmt.Sprintf("sample %d bytes [%d,+%d] outside file of %d bytes",
				idx, s.Offset, s.Size, c.fileSize)).
			AtOffset(s.Offset).AtTick(s.DTS, tr.Timescale).WithState(merr.StateFatal)
	}
	buf := make([]byte, s.Size)
	if _, err := r.ReadAt(buf, s.Offset); err != nil {
		return nil, merr.New(merr.CodeSampleOutOfRange, "sample read failed").
			AtOffset(s.Offset).Wrap(err)
	}
	return buf, nil
}

// validateTopLevel walks top-level headers using the same framing rules as the
// parser and rejects any box whose declared size runs past end of file, or
// whose 8-byte header is itself incomplete.
func validateTopLevel(r io.ReadSeeker, fileSize int64) error {
	for {
		off, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		// A remaining tail shorter than the smallest box header is corruption.
		if fileSize-off > 0 && fileSize-off < 8 {
			return merr.New(merr.CodeTruncatedBox,
				fmt.Sprintf("incomplete box header: only %d of 8 bytes remain", fileSize-off)).
				AtOffset(off).WithState(merr.StateFatal)
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return merr.New(merr.CodeTruncatedBox, "box header truncated").
				AtOffset(off).WithState(merr.StateFatal).Wrap(err)
		}
		end := int64(bi.Offset + bi.Size)
		if end > fileSize {
			return merr.New(merr.CodeTruncatedBox,
				fmt.Sprintf("box %s declares %d bytes but file ends at %d",
					bi.Type.String(), bi.Size, fileSize)).
				AtOffset(int64(bi.Offset)).WithState(merr.StateFatal)
		}
		if _, err := r.Seek(end, io.SeekStart); err != nil {
			return merr.New(merr.CodeInvalidBox, "seek past box").Wrap(err)
		}
	}
}

func containerBox(t mp4.BoxType) bool {
	switch t.String() {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "stsd", "udta", "edts":
		return true
	}
	return false
}
