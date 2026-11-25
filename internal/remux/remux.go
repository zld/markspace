// Package remux writes mapped events into a new MP4 file. Sample payload
// bytes are copied verbatim; only container structure is rebuilt. Every
// written event gets its output byte offset recorded for the manifest.
package remux

import (
	"github.com/abema/go-mp4"

	"mediacore/internal/model"
)

// memSeeker is an in-memory io.WriteSeeker for two-pass box building.
type memSeeker struct {
	buf []byte
	pos int64
}

func (m *memSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		nb := make([]byte, end)
		copy(nb, m.buf)
		m.buf = nb
	}
	copy(m.buf[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memSeeker) Seek(off int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case 0:
		abs = off
	case 1:
		abs = m.pos + off
	case 2:
		abs = int64(len(m.buf)) + off
	}
	m.pos = abs
	return abs, nil
}

func handlerFor(kind model.TrackKind) [4]byte {
	switch kind {
	case model.TrackVideo:
		return [4]byte{'v', 'i', 'd', 'e'}
	case model.TrackAudio:
		return [4]byte{'s', 'o', 'u', 'n'}
	case model.TrackSubtitle:
		return [4]byte{'t', 'e', 'x', 't'}
	}
	return [4]byte{'m', 'e', 't', 'a'}
}

func entryTypeFor(kind model.TrackKind) mp4.BoxType {
	switch kind {
	case model.TrackVideo:
		return mp4.BoxTypeAvc1()
	case model.TrackAudio:
		return mp4.BoxTypeMp4a()
	case model.TrackSubtitle:
		return mp4.BoxTypeWvtt()
	}
	return mp4.StrToBoxType("mett")
}

// isSampleKind reports whether the event becomes a track sample.
func isSampleKind(k model.EventKind) bool {
	return k == model.EventMedia || k == model.EventSubtitle || k == model.EventID3
}

func max64(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
