// Package audio normalizes self-generated PCM: sample-rate conversion,
// channel-layout mapping and loudness control, all on the shared 90 kHz
// time base. Signals are interleaved float32 in the [-1, 1] range.
package audio

import (
	"medkernel/media"
)

// Layout codes mirror the private PSI audio descriptor values.
const (
	LayoutMono   uint8 = 1
	LayoutStereo uint8 = 2
	Layout51     uint8 = 6 // L R C LFE Ls Rs
	Layout71     uint8 = 8 // L R C LFE Ls Rs Lrs Rrs
)

// Frame is one interleaved PCM block stamped with an extended 90 kHz PTS.
type Frame struct {
	Samples  []float32
	Channels int
	Rate     int
	PTS90    int64
}

// LayoutMapper converts between channel layouts. Missing destination
// channels are filled with silence; extra source channels are folded through
// a configured matrix rather than silently dropped.
type LayoutMapper struct {
	SrcCh  int
	DstCh  int
	Matrix [][]float32 // [dst][src] gain; nil selects the built-in default
}

// NewLayoutMapper validates channel counts and builds the default matrix.
func NewLayoutMapper(srcCh, dstCh int, matrix [][]float32) (*LayoutMapper, error) {
	if srcCh < 1 || dstCh < 1 {
		return nil, media.New(media.ClassAudio, "audio.layout",
			"channel count must be positive")
	}
	if matrix != nil {
		if len(matrix) != dstCh {
			return nil, media.New(media.ClassAudio, "audio.layout",
				"matrix must have one row per destination channel")
		}
		for _, row := range matrix {
			if len(row) != srcCh {
				return nil, media.New(media.ClassAudio, "audio.layout",
					"matrix row must have one gain per source channel")
			}
		}
	}
	m := &LayoutMapper{SrcCh: srcCh, DstCh: dstCh, Matrix: matrix}
	if m.Matrix == nil {
		m.Matrix = DefaultMatrix(srcCh, dstCh)
	}
	return m, nil
}

// Process returns a new frame with DstCh channels. The PTS is preserved; the
// sample count per channel stays unchanged.
func (m *LayoutMapper) Process(in Frame) (Frame, error) {
	if in.Channels != m.SrcCh {
		return Frame{}, media.New(media.ClassAudio, "audio.layout",
			"frame channel count does not match mapper").
			WithIndex(int64(in.Channels))
	}
	frames := len(in.Samples) / m.SrcCh
	out := make([]float32, frames*m.DstCh)
	for f := 0; f < frames; f++ {
		srcBase := f * m.SrcCh
		dstBase := f * m.DstCh
		for d := 0; d < m.DstCh; d++ {
			var acc float32
			row := m.Matrix[d]
			for s := 0; s < m.SrcCh; s++ {
				acc += row[s] * in.Samples[srcBase+s]
			}
			out[dstBase+d] = acc
		}
	}
	return Frame{Samples: out, Channels: m.DstCh, Rate: in.Rate, PTS90: in.PTS90}, nil
}

// DefaultMatrix returns the built-in fold/pad matrix. Up-mixes keep the
// source channels at the same indices and add silent channels; down-mixes
// use the ITU-R BS.775 style coefficients for 5.1/7.1 -> stereo and an
// energy-preserving average for every other pair of counts.
func DefaultMatrix(srcCh, dstCh int) [][]float32 {
	m := make([][]float32, dstCh)
	for d := range m {
		m[d] = make([]float32, srcCh)
	}
	switch {
	case srcCh == dstCh:
		for i := 0; i < srcCh; i++ {
			m[i][i] = 1
		}
	case srcCh == 6 && dstCh == 2:
		// L R C LFE Ls Rs -> L R
		m[0][0] = 1
		m[0][2] = 0.7071
		m[0][4] = 0.7071
		m[0][3] = 0.5
		m[1][1] = 1
		m[1][2] = 0.7071
		m[1][5] = 0.7071
		m[1][3] = 0.5
	case srcCh == 8 && dstCh == 2:
		// L R C LFE Ls Rs Lrs Rrs -> L R
		m[0][0] = 1
		m[0][2] = 0.7071
		m[0][4] = 0.5
		m[0][6] = 0.5
		m[0][3] = 0.5
		m[1][1] = 1
		m[1][2] = 0.7071
		m[1][5] = 0.5
		m[1][7] = 0.5
		m[1][3] = 0.5
	case dstCh > srcCh:
		for i := 0; i < srcCh; i++ { // extra channels remain silent zeros
			m[i][i] = 1
		}
	default:
		// Generic fold: each destination receives an equal share of the
		// sources round-robin, scaled to keep energy bounded.
		gain := float32(1) / float32(((srcCh+dstCh-1)/dstCh)+1)
		for s := 0; s < srcCh; s++ {
			m[s%dstCh][s] = gain
		}
	}
	return m
}
