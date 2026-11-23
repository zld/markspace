package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"

	"mediaroute/internal/index"
	"mediaroute/internal/transport"
)

// seekBuffer adapts an in-memory byte slice to io.WriteSeeker for go-mp4.
// Writes always go through indexed assignment (never append-on-gap) so the
// library's backward seek to rewrite a box header cannot shift bytes.
type seekBuffer struct {
	data []byte
	pos  int64
}

func newSeekBuffer(b *[]byte) *seekBuffer {
	return &seekBuffer{data: (*b)[:0]}
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := int(s.pos) + len(p)
	if end > len(s.data) {
		grown := make([]byte, end)
		copy(grown, s.data)
		s.data = grown
	}
	copy(s.data[s.pos:end], p)
	s.pos = int64(end)
	return len(p), nil
}

// Bytes returns the accumulated content.
func (s *seekBuffer) Bytes() []byte { return s.data }

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = s.pos + offset
	case io.SeekEnd:
		pos = int64(len(s.data)) + offset
	}
	if pos < 0 {
		return 0, errors.New("negative seek")
	}
	s.pos = pos
	return pos, nil
}

// sortedSSRCs returns stream keys in ascending order for deterministic runs.
func sortedSSRCs(m map[uint32][]*transport.Frame) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortEntries orders index entries by sample number.
func sortEntries(e []index.Entry) {
	sort.Slice(e, func(i, j int) bool { return e[i].Sample < e[j].Sample })
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func utoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// sha256Hex returns the lowercase hex SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
