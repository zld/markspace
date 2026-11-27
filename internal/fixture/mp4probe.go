package fixture

import (
	"bytes"

	"github.com/abema/go-mp4"
)

// memFile is a full in-memory io.WriteSeeker: writes land at the current
// offset (growing the buffer when needed, overwriting otherwise), which is
// what go-mp4's EndBox relies on to rewrite each box's size header.
type memFile struct {
	buf []byte
	pos int64
}

func (m *memFile) Write(p []byte) (int, error) {
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

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case 0: // io.SeekStart
		target = offset
	case 1: // io.SeekCurrent
		target = m.pos + offset
	default: // io.SeekEnd
		target = int64(len(m.buf)) + offset
	}
	if target < 0 {
		return 0, errNegativeSeek
	}
	m.pos = target
	return target, nil
}

var errNegativeSeek = memFileError("negative seek position")

type memFileError string

func (e memFileError) Error() string { return string(e) }

// freeBox renders an ISO-BMFF 'free' box via the go-mp4 writer, returning
// bytes the pipeline's box walk can independently locate. medtrace's primary
// container is WebM/EBML (internal/ebml); go-mp4 is the pinned auxiliary
// box reader used for side-car ISO media integration.
func freeBox(payload []byte) ([]byte, error) {
	mem := &memFile{}
	w := mp4.NewWriter(mem)
	// AnyTypeBox emits the generic 'free' box without a dedicated struct,
	// exercising the pinned go-mp4 version's generic read/write path.
	bi := &mp4.BoxInfo{Type: mp4.StrToBoxType("free")}
	if _, err := w.StartBox(bi); err != nil {
		return nil, err
	}
	if _, err := w.Write(payload); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return mem.buf, nil
}

// ProbeMP4 writes and re-reads a minimal box structure, returning the box
// type sequence observed through go-mp4's ReadBoxStructure.
func ProbeMP4() ([]string, error) {
	raw, err := freeBox([]byte("medtrace-mp4-helper"))
	if err != nil {
		return nil, err
	}
	var seen []string
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		seen = append(seen, h.BoxInfo.Type.String())
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(raw), handler); err != nil {
		return nil, err
	}
	return seen, nil
}
