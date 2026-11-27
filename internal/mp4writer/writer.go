// Package mp4writer muxes analyzed samples into a minimal, standards-shaped
// MP4 file using github.com/abema/go-mp4. The output is re-readable through
// mp4.Probe, which the tests use to verify track, codec and sample tables.
package mp4writer

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"media-check/internal/stream"
)

// TrackInput carries everything needed to write one MP4 track.
type TrackInput struct {
	Report     *stream.TrackReport
	Width      uint16
	Height     uint16
	Channels   uint16
	SampleRate uint32
	// SampleData is one payload per parsed sample, matching Report.Samples.
	SampleData [][]byte
}

// identityMatrix is the standard 0x00010000 unity transform.
var identityMatrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

const boxHeaderSize = 8

// Write serializes the tracks into a self-contained MP4 byte slice.
// Layout: ftyp, moov (with measured stco offsets), mdat.
func Write(tracks []TrackInput) ([]byte, error) {
	if len(tracks) == 0 {
		return nil, fmt.Errorf("mp4writer: no tracks to write")
	}
	ftypBytes, err := buildFtyp()
	if err != nil {
		return nil, err
	}
	mdatBytes, err := buildMdat(tracks)
	if err != nil {
		return nil, err
	}
	// Pass 1: build moov with a placeholder offset so its size is known.
	probeOffsets := make([]uint32, len(tracks))
	firstMoov, err := buildMoovBytes(tracks, probeOffsets)
	if err != nil {
		return nil, err
	}
	// Each track holds one contiguous chunk; offsets advance track by track.
	dataStart := uint32(len(ftypBytes) + len(firstMoov) + boxHeaderSize)
	offsets := make([]uint32, len(tracks))
	cursor := dataStart
	for i, tk := range tracks {
		offsets[i] = cursor
		for _, d := range tk.SampleData {
			cursor += uint32(len(d))
		}
	}
	// Pass 2: rebuild moov with the real chunk offsets. Same box sizes.
	moovBytes, err := buildMoovBytes(tracks, offsets)
	if err != nil {
		return nil, err
	}
	if len(moovBytes) != len(firstMoov) {
		return nil, fmt.Errorf("mp4writer: moov size drift between passes (%d vs %d)",
			len(firstMoov), len(moovBytes))
	}

	out := make([]byte, 0, len(ftypBytes)+len(moovBytes)+len(mdatBytes))
	out = append(out, ftypBytes...)
	out = append(out, moovBytes...)
	out = append(out, mdatBytes...)
	return out, nil
}

// memSeeker is a sparse in-memory io.WriteSeeker with an absolute position.
// Seeking backwards and overwriting earlier bytes (as go-mp4's EndBox does to
// patch each box size) must never truncate later content.
type memSeeker struct {
	buf []byte
	pos int64
}

func (m *memSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if int(end) > len(m.buf) {
		// Grow (preserving any bytes already written beyond the current pos).
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = offset
	case io.SeekCurrent:
		m.pos += offset
	case io.SeekEnd:
		m.pos = int64(len(m.buf)) + offset
	}
	if m.pos < 0 {
		return 0, fmt.Errorf("mp4writer: negative seek position")
	}
	return m.pos, nil
}

func (m *memSeeker) bytes() []byte { return m.buf }

// buildMdat concatenates every track's sample payloads into one mdat box.
func buildMdat(tracks []TrackInput) ([]byte, error) {
	total := 0
	for _, tk := range tracks {
		for _, d := range tk.SampleData {
			total += len(d)
		}
	}
	b := newBuilder()
	if err := b.start(mp4.BoxTypeMdat()); err != nil {
		return nil, err
	}
	for _, tk := range tracks {
		for _, d := range tk.SampleData {
			if _, err := b.w.Write(d); err != nil {
				return nil, err
			}
		}
	}
	if err := b.end(); err != nil {
		return nil, err
	}
	return b.bytes(), nil
}
