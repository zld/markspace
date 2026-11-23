package render

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"
)

// BoxSummary describes a synthesized MP4-style metadata box. It exists so the
// recovery export can emit a structured container alongside the PNG frames
// without re-encoding any media (per the no-transcode constraint).
type BoxSummary struct {
	Type string
	Size int
}

// seekBuffer is an in-memory io.WriteSeeker backed by a byte buffer.
type seekBuffer struct {
	buf bytes.Buffer
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	// Support the back-patching writes go-mp4 performs at box end.
	end := s.pos + int64(len(p))
	if end > int64(len(s.buf.Bytes())) {
		grow := make([]byte, end-int64(s.buf.Len()))
		s.buf.Write(grow)
	}
	n := copy(s.buf.Bytes()[s.pos:], p)
	s.pos += int64(n)
	return n, nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = offset
	case io.SeekCurrent:
		s.pos += offset
	case io.SeekEnd:
		s.pos = int64(s.buf.Len()) + offset
	}
	return s.pos, nil
}

// BuildManifestBoxes serializes a small MP4 box hierarchy using go-mp4: a
// top-level 'mdat' holding the per-frame SHA-256 records. The output is fully
// deterministic for an identical manifest.
func BuildManifestBoxes(hashes []FrameHash) ([]byte, []BoxSummary, error) {
	var payload bytes.Buffer
	for _, h := range hashes {
		var ts [8]byte
		binary.BigEndian.PutUint64(ts[:], uint64(h.TimeNs))
		payload.Write(ts[:])
		payload.Write([]byte(h.SHA256))
	}

	sb := &seekBuffer{}
	w := mp4.NewWriter(sb)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, nil, err
	}
	if _, err := w.Write(payload.Bytes()); err != nil {
		return nil, nil, err
	}
	bi, err := w.EndBox()
	if err != nil {
		return nil, nil, err
	}
	raw := sb.buf.Bytes()
	summary := []BoxSummary{{Type: "mdat", Size: int(bi.Size)}}
	return raw, summary, nil
}
