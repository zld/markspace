// Package pixel implements the project's deterministic frame format: synthetic
// RGB planes, a small RLE payload and portable statistics. Fixtures, render and
// verify all use it, so any disagreement is a real defect rather than a
// difference in interpretation.
package pixel

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
)

// Magic marks an encoded frame payload.
var Magic = [4]byte{'M', 'L', 'P', '1'}

// HeaderSize is the fixed prefix of an encoded payload.
const HeaderSize = 20

// Raw is an 8-bit RGB image in row-major order.
type Raw struct {
	W, H int
	Pix  []byte // len(Pix) == W*H*3
}

// Bounds is the accepted frame geometry.
const (
	MinDim = 16
	MaxDim = 4096
)

// Validate checks geometry and buffer length.
func (r Raw) Validate() error {
	if err := r.validateGeometry(); err != nil {
		return err
	}
	if len(r.Pix) != r.W*r.H*3 {
		return fmt.Errorf("pixel: buffer is %d bytes, want %d", len(r.Pix), r.W*r.H*3)
	}
	return nil
}

func (r Raw) validateGeometry() error {
	if r.W < MinDim || r.H < MinDim || r.W > MaxDim || r.H > MaxDim {
		return fmt.Errorf("pixel: dimensions %dx%d outside [%d,%d]", r.W, r.H, MinDim, MaxDim)
	}
	return nil
}

// Stats is the portable per-frame summary printed by the CLI and re-derived
// by the verifier from decoded artefacts.
type Stats struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	MeanR    float64 `json:"meanR"`
	MeanG    float64 `json:"meanG"`
	MeanB    float64 `json:"meanB"`
	Variance float64 `json:"variance"`
	Min      uint8   `json:"min"`
	Max      uint8   `json:"max"`
}

// Measure computes exact integer sums so results do not depend on float
// accumulation order.
func Measure(r Raw) (Stats, error) {
	if err := r.Validate(); err != nil {
		return Stats{}, err
	}
	n := uint64(r.W * r.H)
	var sr, sg, sb, sumSq uint64
	minV, maxV := uint8(255), uint8(0)
	for i := 0; i < len(r.Pix); i += 3 {
		rv, gv, bv := r.Pix[i], r.Pix[i+1], r.Pix[i+2]
		sr += uint64(rv)
		sg += uint64(gv)
		sb += uint64(bv)
		for _, v := range [...]uint8{rv, gv, bv} {
			sumSq += uint64(v) * uint64(v)
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
	}
	mean := float64(sr+sg+sb) / float64(3*n)
	sqMean := float64(sumSq) / float64(3*n)
	variance := sqMean - mean*mean
	if variance < 0 {
		variance = 0
	}
	return Stats{
		Width: r.W, Height: r.H,
		MeanR:    float64(sr) / float64(n),
		MeanG:    float64(sg) / float64(n),
		MeanB:    float64(sb) / float64(n),
		Variance: math.Round(variance*1e6) / 1e6,
		Min:      minV,
		Max:      maxV,
	}, nil
}

// checksum covers the raw planes with CRC-32 for in-payload corruption
// detection; SHA-256 covers the whole file at the manifest layer.
func checksum(r Raw) uint32 {
	return crc32.ChecksumIEEE(r.Pix)
}

// Quantise rounds every channel to a multiple of step. step=1 is lossless.
// The planner uses it to trade payload size for pixel fidelity.
func Quantise(r Raw, step int) Raw {
	if step <= 1 {
		return r
	}
	if step > 255 {
		step = 255
	}
	out := Raw{W: r.W, H: r.H, Pix: make([]byte, len(r.Pix))}
	half := step / 2
	ceiling := 255 / step * step
	for i, v := range r.Pix {
		q := (int(v) + half) / step * step
		if q > ceiling {
			q = ceiling
		}
		out.Pix[i] = byte(q)
	}
	return out
}

// Quality is the documented pixel-fidelity score of a quantisation step.
func Quality(step int) float64 {
	if step <= 1 {
		return 1
	}
	return 1 - float64(step-1)/256
}

// StepForQuality returns the quantisation step nearest to a quality score,
// clamped to [1,255].
func StepForQuality(q float64) int {
	if q >= 1 {
		return 1
	}
	step := int(math.Round((1-q)*256)) + 1
	if step < 1 {
		step = 1
	}
	if step > 255 {
		step = 255
	}
	return step
}

// Encode serialises a raw image. Format:
//
//	magic[4] | w u16 | h u16 | track u16 | seq u32 | crc32 u32 | plane RLE x3
//
// Each plane is a sequence of tokens: 0x00 value count_u16 (run of >=4) or
// 0x01 n_u8 bytes (literal run of 1..255).
func Encode(r Raw, track int, seq uint32) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if track <= 0 || track > 0xffff {
		return nil, fmt.Errorf("pixel: track %d out of range", track)
	}
	out := make([]byte, HeaderSize, HeaderSize+r.W*r.H)
	copy(out, Magic[:])
	binary.BigEndian.PutUint16(out[4:], uint16(r.W))
	binary.BigEndian.PutUint16(out[6:], uint16(r.H))
	binary.BigEndian.PutUint16(out[8:], uint16(track))
	binary.BigEndian.PutUint32(out[10:], seq)
	binary.BigEndian.PutUint32(out[14:], checksum(r))
	out = out[:HeaderSize]
	for plane := 0; plane < 3; plane++ {
		out = appendPlane(out, r, plane)
	}
	return out, nil
}

func appendPlane(dst []byte, r Raw, plane int) []byte {
	count := r.W * r.H
	i := 0
	for i < count {
		val := r.Pix[i*3+plane]
		run := 1
		for i+run < count && r.Pix[(i+run)*3+plane] == val && run < 0xffff {
			run++
		}
		if run >= 4 {
			dst = append(dst, 0x00, val, byte(run>>8), byte(run))
			i += run
			continue
		}
		// Literal run: gather until the next long run starts.
		start := i
		i += run
		for i < count && i-start < 255 {
			v := r.Pix[i*3+plane]
			next := 1
			for i+next < count && r.Pix[(i+next)*3+plane] == v && next < 4 {
				next++
			}
			if next >= 4 {
				break
			}
			i++
		}
		dst = append(dst, 0x01, byte(i-start))
		for j := start; j < i; j++ {
			dst = append(dst, r.Pix[j*3+plane])
		}
	}
	return dst
}

// Decode parses and validates an encoded payload, returning the raw image and
// declared track/sequence. Corrupt payloads return an error, never a guess.
func Decode(b []byte) (Raw, int, uint32, error) {
	if len(b) < HeaderSize {
		return Raw{}, 0, 0, fmt.Errorf("pixel: payload truncated (%d bytes)", len(b))
	}
	if [4]byte(b[0:4]) != Magic {
		return Raw{}, 0, 0, fmt.Errorf("pixel: bad magic %q", b[0:4])
	}
	w := int(binary.BigEndian.Uint16(b[4:]))
	h := int(binary.BigEndian.Uint16(b[6:]))
	track := int(binary.BigEndian.Uint16(b[8:]))
	seq := binary.BigEndian.Uint32(b[10:])
	want := binary.BigEndian.Uint32(b[14:])
	r := Raw{W: w, H: h}
	if err := r.validateGeometry(); err != nil {
		return Raw{}, 0, 0, err
	}
	r.Pix = make([]byte, w*h*3)
	rest := b[HeaderSize:]
	for plane := 0; plane < 3; plane++ {
		var err error
		rest, err = decodePlane(rest, r, plane)
		if err != nil {
			return Raw{}, 0, 0, err
		}
	}
	if len(rest) != 0 {
		return Raw{}, 0, 0, fmt.Errorf("pixel: %d trailing bytes", len(rest))
	}
	if got := checksum(r); got != want {
		return Raw{}, 0, 0, fmt.Errorf("pixel: checksum %08x != declared %08x", got, want)
	}
	return r, track, seq, nil
}

func decodePlane(src []byte, r Raw, plane int) ([]byte, error) {
	count := r.W * r.H
	pos := 0
	for pos < count {
		if len(src) < 2 {
			return nil, fmt.Errorf("pixel: plane %d truncated at sample %d", plane, pos)
		}
		token := src[0]
		switch token {
		case 0x00:
			if len(src) < 4 {
				return nil, fmt.Errorf("pixel: plane %d run header truncated", plane)
			}
			val := src[1]
			run := int(binary.BigEndian.Uint16(src[2:]))
			if run < 4 {
				return nil, fmt.Errorf("pixel: plane %d run length %d below 4", plane, run)
			}
			if pos+run > count {
				return nil, fmt.Errorf("pixel: plane %d run overruns image", plane)
			}
			for j := 0; j < run; j++ {
				r.Pix[(pos+j)*3+plane] = val
			}
			pos += run
			src = src[4:]
		case 0x01:
			n := int(src[1])
			if n == 0 {
				return nil, fmt.Errorf("pixel: plane %d empty literal run", plane)
			}
			if len(src) < 2+n {
				return nil, fmt.Errorf("pixel: plane %d literal truncated", plane)
			}
			if pos+n > count {
				return nil, fmt.Errorf("pixel: plane %d literal overruns image", plane)
			}
			for j := 0; j < n; j++ {
				r.Pix[(pos+j)*3+plane] = src[2+j]
			}
			pos += n
			src = src[2+n:]
		default:
			return nil, fmt.Errorf("pixel: plane %d unknown token %#x", plane, token)
		}
	}
	return src, nil
}
