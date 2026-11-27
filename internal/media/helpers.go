// Package media: container layout helpers.
package media

import (
	"encoding/binary"

	"github.com/abema/go-mp4"

	"example.com/assetpack/internal/audio"
	"example.com/assetpack/internal/fault"
)

func start(w *mp4.Writer, typ mp4.BoxType) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return wrap(typ.String(), err)
	}
	return nil
}

func wrap(what string, err error) error {
	return fault.New(fault.StageAudio, fault.KindIO, "write %s box: %v", what, err)
}

func interleave(p audio.PCM) []byte {
	frames := p.Frames()
	ch := len(p.Channels)
	out := make([]byte, frames*ch*2)
	pos := 0
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			v := p.Channels[c][i]
			if v > 1 {
				v = 1
			} else if v < -1 {
				v = -1
			}
			s := int16(v * 32767)
			binary.LittleEndian.PutUint16(out[pos:], uint16(s))
			pos += 2
		}
	}
	return out
}

type layoutInfo struct {
	sizes []int // per-sample byte sizes
	last  int   // frames in the final (possibly short) sample
	count int
}

func buildLayout(frames, channels int) layoutInfo {
	perFrame := channels * 2
	var li layoutInfo
	remaining := frames
	for remaining > 0 {
		n := ChunkFrames
		if remaining < n {
			n = remaining
		}
		li.sizes = append(li.sizes, n*perFrame)
		li.last = n
		remaining -= n
	}
	li.count = len(li.sizes)
	return li
}
