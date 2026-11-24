// Package audio performs deterministic in-project sample-rate conversion,
// channel normalization, loudness measurement and gain control. It has no
// dependency on system codecs.
package audio

import (
	"fmt"
	"strings"

	"mediad/internal/mederr"
)

// Layout describes a channel layout. Channels are stored planar: one
// []float64 per channel. The 5.1 order is L, R, C, LFE, Ls, Rs.
type Layout struct {
	Name     string `json:"name"`
	Channels int    `json:"channels"`
}

var (
	LayoutMono    = Layout{Name: "mono", Channels: 1}
	LayoutStereo  = Layout{Name: "stereo", Channels: 2}
	LayoutFiveOne = Layout{Name: "5.1", Channels: 6}
)

// ParseLayout accepts the distribution layout names used by the CLI.
func ParseLayout(s string) (Layout, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mono", "1":
		return LayoutMono, nil
	case "stereo", "2", "2.0":
		return LayoutStereo, nil
	case "5.1", "six":
		return LayoutFiveOne, nil
	default:
		return Layout{}, mederr.New(mederr.CatAudio, "audio", -1, -1, false,
			"unsupported channel layout %q", s)
	}
}

// Buffer is planar floating-point PCM in [-1,1].
type Buffer struct {
	Rate   int         `json:"rate"`
	Layout Layout      `json:"layout"`
	Planar [][]float64 `json:"-"`
}

// NewBuffer allocates zeroed planar PCM.
func NewBuffer(rate int, layout Layout, frames int) (*Buffer, error) {
	if rate <= 0 {
		return nil, mederr.New(mederr.CatAudio, "audio", -1, -1, false, "sample rate must be positive")
	}
	if layout.Channels <= 0 || frames < 0 {
		return nil, mederr.New(mederr.CatAudio, "audio", -1, -1, false,
			"invalid buffer shape rate=%d channels=%d frames=%d", rate, layout.Channels, frames)
	}
	ch := make([][]float64, layout.Channels)
	for i := range ch {
		ch[i] = make([]float64, frames)
	}
	return &Buffer{Rate: rate, Layout: layout, Planar: ch}, nil
}

// MustNew is for in-repo deterministic generators after constants are validated.
func MustNew(rate int, layout Layout, frames int) *Buffer {
	b, err := NewBuffer(rate, layout, frames)
	if err != nil {
		panic(err)
	}
	return b
}

// Frames returns the number of samples per channel.
func (b *Buffer) Frames() int {
	if b == nil || len(b.Planar) == 0 {
		return 0
	}
	return len(b.Planar[0])
}

// Validate checks shape consistency and rejects NaN/Inf.
func (b *Buffer) Validate() error {
	if b == nil || b.Rate <= 0 {
		return mederr.New(mederr.CatAudio, "audio", -1, -1, false, "nil or zero-rate audio buffer")
	}
	if len(b.Planar) != b.Layout.Channels {
		return mederr.New(mederr.CatAudio, "audio", -1, -1, false,
			"layout %q expects %d channels, got %d", b.Layout.Name, b.Layout.Channels, len(b.Planar))
	}
	n := b.Frames()
	for c, ch := range b.Planar {
		if len(ch) != n {
			return mederr.New(mederr.CatAudio, fmt.Sprintf("ch:%d", c), -1, -1, false,
				"unequal channel lengths %d and %d", len(ch), n)
		}
		for i, v := range ch {
			if v != v || v > 1e300 || v < -1e300 {
				return mederr.New(mederr.CatAudio, fmt.Sprintf("ch:%d", c), -1, -1, false,
					"non-finite sample at frame %d", i)
			}
		}
	}
	return nil
}

// Clone performs a deep copy.
func (b *Buffer) Clone() *Buffer {
	out := &Buffer{Rate: b.Rate, Layout: b.Layout, Planar: make([][]float64, len(b.Planar))}
	for i := range b.Planar {
		out.Planar[i] = append([]float64(nil), b.Planar[i]...)
	}
	return out
}

// ConvertLayout maps a buffer to the requested distribution layout using
// fixed deterministic downmix/upmix coefficients.
func ConvertLayout(in *Buffer, target Layout) (*Buffer, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.Layout.Channels == target.Channels {
		out := in.Clone()
		out.Layout = target
		return out, nil
	}
	frames := in.Frames()
	out := MustNew(in.Rate, target, frames)
	for i := 0; i < frames; i++ {
		src := make([]float64, in.Layout.Channels)
		for c := range src {
			src[c] = in.Planar[c][i]
		}
		dst := convertFrame(src, in.Layout, target)
		for c := 0; c < target.Channels; c++ {
			out.Planar[c][i] = dst[c]
		}
	}
	return out, nil
}

func convertFrame(src []float64, from, to Layout) []float64 {
	dst := make([]float64, to.Channels)
	switch to.Channels {
	case 1:
		switch from.Channels {
		case 2:
			dst[0] = 0.5 * (src[0] + src[1])
		case 6:
			// Standard-ish downmix to mono.
			dst[0] = 0.707*(src[0]+src[1]) + src[2] +
				0.5*src[3] + 0.707*(src[4]+src[5])
			dst[0] /= 2.0
		default:
			var sum float64
			for _, v := range src {
				sum += v
			}
			dst[0] = sum / float64(len(src))
		}
	case 2:
		switch from.Channels {
		case 1:
			dst[0], dst[1] = src[0], src[0]
		case 6:
			c := 0.707 * src[2]
			dst[0] = src[0] + c + 0.707*src[4] + 0.5*src[3]
			dst[1] = src[1] + c + 0.707*src[5] + 0.5*src[3]
		default:
			for c := range dst {
				dst[c] = src[0]
			}
		}
	case 6:
		switch from.Channels {
		case 1:
			dst[2] = src[0] // center
		case 2:
			dst[0], dst[1] = src[0], src[1]
		default:
			copy(dst, src)
		}
	}
	return dst
}
