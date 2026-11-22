// Package pipeline orchestrates the full mediq flow: synthetic sample
// generation, bitstream scan, unified timeline, index reconciliation,
// segment planning and cross-representation alignment, transactional
// commits, manifest round-trip verification and hash reporting.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"

	"mediq/internal/bitstream"
	"mediq/internal/index"
	"mediq/internal/timeline"
)

// RepresentationSpec describes one generated bitrate layer.
type RepresentationSpec struct {
	ID    string
	Codec bitstream.Codec
	Skew  timeline.Ticks // PTS offset versus the master timeline
}

// DefaultRepresentations returns H.264, H.265 and AV1 layers.
func DefaultRepresentations() []RepresentationSpec {
	return []RepresentationSpec{
		{ID: "h264-720p", Codec: bitstream.CodecH264},
		{ID: "h265-1080p", Codec: bitstream.CodecH265},
		{ID: "av1-1080p", Codec: bitstream.CodecAV1, Skew: timeline.MustMs(5)},
	}
}

// Asset is one generated representation: bytes plus its true index.
type Asset struct {
	Spec    RepresentationSpec
	Data    []byte
	Entries []index.Entry
}

// FaultSet selects the injected damage classes.
type FaultSet struct {
	Truncate       bool // cut the tail of every stream
	DropIndex      bool // remove index rows
	DuplicateIndex bool // repeat an index row
	CorruptUnit    bool // flip payload bytes of one unit
	Misalign       bool // push one representation past the skew tolerance
}

// Any reports whether any fault is selected.
func (f FaultSet) Any() bool {
	return f.Truncate || f.DropIndex || f.DuplicateIndex || f.CorruptUnit || f.Misalign
}

// SampleParams controls deterministic generation.
type SampleParams struct {
	Seed          int64
	Keyframes     int // key frames per representation
	InterPerKey   int // inter units between key frames
	FrameDuration timeline.Ticks
}

// DefaultSampleParams yields 26 key frames per representation at 40 ms
// frame spacing (3 frames per GOP, 120 ms per key), enough for 20+
// segments per layer once the 160 ms segment target is applied.
func DefaultSampleParams(seed int64) SampleParams {
	return SampleParams{
		Seed:          seed,
		Keyframes:     26,
		InterPerKey:   2,
		FrameDuration: timeline.MustMs(40),
	}
}

// Generate builds clean sample assets and their keyframe indexes.
func Generate(specs []RepresentationSpec, p SampleParams) ([]*Asset, error) {
	rng := rand.New(rand.NewSource(p.Seed))
	out := make([]*Asset, 0, len(specs))
	for _, spec := range specs {
		a := &Asset{Spec: spec}
		seq := uint32(0)
		for k := 0; k < p.Keyframes; k++ {
			base := timeline.Ticks(k) * p.FrameDuration * timeline.Ticks(p.InterPerKey+1)
			kpts := base + spec.Skew
			body := bodyFor(rng, 48)
			a.Entries = append(a.Entries, index.Entry{
				Seq: seq, Offset: int64(len(a.Data)), PTS: kpts,
			})
			a.appendUnit(spec.Codec, seq, kpts, true, body)
			seq++
			for j := 1; j <= p.InterPerKey; j++ {
				pts := kpts + timeline.Ticks(j)*p.FrameDuration
				a.appendUnit(spec.Codec, seq, pts, false, bodyFor(rng, 32))
				seq++
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func (a *Asset) appendUnit(codec bitstream.Codec, seq uint32, pts timeline.Ticks, key bool, body []byte) {
	a.Data = append(a.Data, encodeUnit(codec, seq, pts, key, body)...)
}

// encodeUnit renders one synthetic unit in the codec's framing.
func encodeUnit(codec bitstream.Codec, seq uint32, pts timeline.Ticks, key bool, body []byte) []byte {
	switch codec {
	case bitstream.CodecAV1:
		framed := append([]byte{boolByte(key)}, body...)
		meta := bitstream.EncodeMeta(seq, pts, uint32(len(framed)), bitstream.CRC32(framed))
		payload := append(meta[:], framed...)
		return append([]byte{byte(6<<3) | 0x02, byte(len(payload))}, payload...)
	default:
		hdr := []byte{1}
		if codec == bitstream.CodecH265 {
			hdr = []byte{1 << 1, 1}
		}
		if key {
			if codec == bitstream.CodecH264 {
				hdr[0] = 5
			} else {
				hdr[0] = 19 << 1
			}
		}
		meta := bitstream.EncodeMeta(seq, pts, uint32(len(body)), bitstream.CRC32(body))
		inner := append(append([]byte{}, meta[:]...), body...)
		out := append([]byte{0, 0, 0, 1}, hdr...)
		return append(out, bitstream.EscapeEPB(inner)...)
	}
}

func bodyFor(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	if _, err := rng.Read(b); err != nil {
		panic(err) // rand.Rand.Read never errors
	}
	return b
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// corruptBase returns the byte offset at which the first unit's
// payload begins, accounting for codec framing and the MDIQ meta.
func corruptBase(codec bitstream.Codec) int {
	switch codec {
	case bitstream.CodecAV1:
		return 1 + 1 + bitstream.MetaLen // OBU header, LEB128, meta
	case bitstream.CodecH265:
		return 4 + 2 + bitstream.MetaLen // start code, 2 NAL bytes, meta
	default:
		return 4 + 1 + bitstream.MetaLen // start code, 1 NAL byte, meta
	}
}

// SHA256Hex returns the hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ParseFaults maps CLI fault names to a FaultSet.
func ParseFaults(names []string) (FaultSet, error) {
	var f FaultSet
	for _, n := range names {
		switch n {
		case "truncate":
			f.Truncate = true
		case "drop-index":
			f.DropIndex = true
		case "duplicate-index":
			f.DuplicateIndex = true
		case "corrupt-unit":
			f.CorruptUnit = true
		case "misalign":
			f.Misalign = true
		case "":
		default:
			return f, fmt.Errorf("unknown fault injection %q", n)
		}
	}
	return f, nil
}
