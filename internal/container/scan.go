// Package container scans MP4/MOV (ISOBMFF) files into track metadata and
// per-sample timing. Every declared length that runs past EOF and every
// sample-table inconsistency is reported with its byte offset and a stable
// error code from internal/errors.
package container

import (
	"io"
	"os"
	"strings"

	"github.com/abema/go-mp4"

	"mediatool/internal/clock"
	merr "mediatool/internal/errors"
)

// Kind identifies a track's media type.
type Kind int

const (
	KindUnknown Kind = iota
	KindVideo
	KindAudio
)

// Chunk describes one sample-to-chunk run from the stsc table.
type Chunk struct {
	FirstChunk      uint32
	SamplesPerChunk uint32
}

// Track holds one trak box's scan result.
type Track struct {
	ID        uint32
	Kind      Kind
	Timescale uint32
	Duration  uint64
	Samples   []clock.Sample
	chunks    []Chunk
}

// File is the result of scanning one MP4/MOV file.
type File struct {
	Path       string
	Size       int64
	Timescale  uint32
	Tracks     []*Track
	MdatOffset int64
	MdatSize   uint64
}

type scanState struct {
	fileSize int64
	tracks   []*Track
	cur      *Track
	mdatOff  int64
	mdatSize uint64
	mvhd     *mp4.Mvhd
}

// isContainer reports box types whose only content is child boxes.
func isContainer(t mp4.BoxType) bool {
	switch t.String() {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts", "udta":
		return true
	}
	return false
}

// Scan opens path and fully scans its box structure and sample tables.
func Scan(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, merr.New(merr.CodeInvalidBox, "open %s: %v", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, merr.New(merr.CodeInvalidBox, "stat: %v", err)
	}
	stt := &scanState{fileSize: st.Size()}
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		bi := h.BoxInfo
		if err := stt.checkBounds(bi.Offset, bi.Size); err != nil {
			return nil, err
		}
		if bi.Type == mp4.BoxTypeMdat() {
			stt.mdatOff = int64(bi.Offset + bi.HeaderSize)
			stt.mdatSize = bi.Size - bi.HeaderSize
			return nil, nil
		}
		// Container boxes carry no payload of their own: recurse into them.
		if isContainer(bi.Type) {
			if _, err := h.Expand(); err != nil {
				var me *merr.MediaError
				if merr.As(err, &me) {
					return nil, err // preserve the inner stable code/offset
				}
				return nil, merr.At(merr.CodeInvalidBox, int64(bi.Offset),
					"expand %s: %v", bi.Type.String(), err)
			}
			return nil, nil
		}
		if err := stt.checkPayloadBounds(bi); err != nil {
			return nil, err
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, merr.At(merr.CodeInvalidBox, int64(bi.Offset),
				"parse %s: %v", bi.Type.String(), err)
		}
		return nil, stt.dispatch(h, box)
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		if _, ok := err.(*merr.MediaError); ok {
			return nil, err
		}
		// Retain the current read position as the detection offset when
		// the generic parser rejects a malformed box.
		off := int64(-1)
		if pos, perr := f.Seek(0, io.SeekCurrent); perr == nil {
			off = pos
		}
		return nil, merr.At(merr.CodeInvalidBox, off, "box structure: %v", err)
	}
	if len(stt.tracks) == 0 {
		return nil, merr.New(merr.CodeInvalidBox, "no trak boxes found")
	}
	out := &File{
		Path:       path,
		Size:       stt.fileSize,
		Tracks:     stt.tracks,
		MdatOffset: stt.mdatOff,
		MdatSize:   stt.mdatSize,
	}
	if stt.mvhd != nil {
		out.Timescale = stt.mvhd.Timescale
	}
	return out, nil
}

func (s *scanState) checkBounds(offset, size uint64) error {
	end := int64(offset + size)
	if offset+size < offset || end > s.fileSize {
		return merr.At(merr.CodeBoxOutOfRange, int64(offset),
			"box ends at %d beyond file size %d", end, s.fileSize)
	}
	return nil
}

// checkPayloadBounds distinguishes a truncated box (declared size past EOF)
// from an internal parse failure before the generic unmarshaller runs.
func (s *scanState) checkPayloadBounds(bi mp4.BoxInfo) error {
	end := int64(bi.Offset + bi.Size)
	if end > s.fileSize {
		return merr.At(merr.CodeBoxOutOfRange, int64(bi.Offset),
			"%s box ends at %d beyond EOF %d", bi.Type.String(), end, s.fileSize)
	}
	return nil
}

func (s *scanState) dispatch(h *mp4.ReadHandle, box mp4.IBox) error {
	off := int64(h.BoxInfo.Offset)
	switch b := box.(type) {
	case *mp4.Mvhd:
		s.mvhd = b
	case *mp4.Tkhd:
		if s.cur == nil {
			s.cur = &Track{}
			s.tracks = append(s.tracks, s.cur)
		}
		s.cur.ID = b.TrackID
	case *mp4.Hdlr:
		if s.cur == nil {
			s.cur = &Track{}
			s.tracks = append(s.tracks, s.cur)
		}
		switch strings.TrimSpace(string(b.HandlerType[:])) {
		case "vide":
			s.cur.Kind = KindVideo
		case "soun":
			s.cur.Kind = KindAudio
		}
	case *mp4.Mdhd:
		s.cur.Timescale = b.Timescale
		s.cur.Duration = b.GetDuration()
	case *mp4.Stts:
		return s.cur.applyStts(b, off)
	case *mp4.Ctts:
		return s.cur.applyCtts(b)
	case *mp4.Stsc:
		for _, e := range b.Entries {
			s.cur.chunks = append(s.cur.chunks, Chunk{e.FirstChunk, e.SamplesPerChunk})
		}
	case *mp4.Stsz:
		return s.cur.applySizes(b, off)
	case *mp4.Stco:
		return s.cur.applyOffsets32(b.ChunkOffset, off, s.fileSize)
	case *mp4.Co64:
		return s.cur.applyOffsets64(b.ChunkOffset, off, s.fileSize)
	case *mp4.Stss:
		return s.cur.applyKeyframes(b)
	}
	return nil
}

func (t *Track) applyStts(b *mp4.Stts, off int64) error {
	n := 0
	for _, e := range b.Entries {
		n += int(e.SampleCount)
	}
	if cap(t.Samples) < n {
		t.Samples = make([]clock.Sample, 0, n)
	}
	var dtsTicks int64
	idx := 0
	for _, e := range b.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			dns, err := clock.ScaleToNanos(t.Timescale, dtsTicks)
			if err != nil {
				return merr.At(merr.CodeInvalidBox, off, "stts: %v", err)
			}
			dur, err := clock.ScaleToNanos(t.Timescale, int64(e.SampleDelta))
			if err != nil {
				return err
			}
			t.Samples = append(t.Samples, clock.Sample{
				Index: idx, DTS: dns, PTS: dns, Duration: dur,
			})
			dtsTicks += int64(e.SampleDelta)
			idx++
		}
	}
	return nil
}

func (t *Track) applyCtts(b *mp4.Ctts) error {
	idx := 0
	for ei, e := range b.Entries {
		offTicks, err := clock.ScaleToNanos(t.Timescale, b.GetSampleOffset(ei))
		if err != nil {
			return err
		}
		for k := uint32(0); k < e.SampleCount && idx < len(t.Samples); k, idx = k+1, idx+1 {
			t.Samples[idx].PTS = t.Samples[idx].DTS + offTicks
		}
	}
	if idx != len(t.Samples) {
		return merr.New(merr.CodeInvalidBox, "ctts covers %d of %d samples", idx, len(t.Samples))
	}
	return nil
}
