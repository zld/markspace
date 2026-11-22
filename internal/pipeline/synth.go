// Package pipeline orchestrates the full mediq flow: synthetic sample
// generation, bitstream scan, unified timeline, index reconciliation,
// segment planning and cross-representation alignment, transactional
// commits, manifest round-trip verification and hash reporting.
package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
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
	Truncate      bool // cut the tail of every stream
	DropIndex     bool // remove index rows
	DuplicateIndex bool // repeat an index row
	CorruptUnit   bool // flip payload bytes of one unit
	Misalign      bool // push one representation past the skew tolerance
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

// Inject applies the selected faults deterministically and returns the
// count of each class so the run report can prove what was injected.
// Ordering is fixed so faults compose instead of cancelling each other:
// misalign (PTS rewrite on the full stream) -> corrupt -> truncate ->
// index edits. Byte offsets never move under misalign, so every
// representation keeps the same boundary count and the aligner fails on
// the time-skew tolerance, not on a structural count mismatch.
func Inject(assets []*Asset, faults FaultSet, p SampleParams) FaultCounts {
	var c FaultCounts
	rng := rand.New(rand.NewSource(p.Seed + 99))
	if faults.Misalign {
		for _, a := range assets {
			if a.Spec.Codec != bitstream.CodecAV1 {
				continue
			}
			delta := timeline.MustMs(120) // well beyond the 40 ms tolerance
			shiftPTS(a, delta)
			a.Spec.Skew += delta
			c.Misaligned++
		}
	}
	for _, a := range assets {
		if faults.CorruptUnit {
			// Flip bytes inside the payload region of the first full
			// unit (behind start code, NAL/OBU header and MDIQ meta).
			// Framing stays intact, so the scanner quarantines the unit
			// instead of mistaking it for truncation.
			base := corruptBase(a.Spec.Codec)
			if len(a.Data) > base+8 {
				pos := base + rng.Intn(8)
				for j := 0; j < 3; j++ {
					a.Data[pos+j] ^= 0xa5
				}
				c.Corrupt++
			}
		}
		if faults.Truncate {
			// Cut only the final unit, keeping the damaged first unit
			// fully on disk so both fault classes are observable.
			cut := 16 + rng.Intn(8)
			if len(a.Data) > cut*4 {
				a.Data = a.Data[:len(a.Data)-cut]
				c.Truncated++
			}
		}
		if faults.DropIndex && len(a.Entries) >= 4 {
			// Remove two scattered index rows; reconcile must recover
			// both keyframes from the bitstream.
			keep := a.Entries[:0]
			dropped := 0
			for i, e := range a.Entries {
				if (i == 2 || i == len(a.Entries)-2) && dropped < 2 {
					dropped++
					continue
				}
				keep = append(keep, e)
			}
			a.Entries = keep
			c.MissingIndex += dropped
		}
		if faults.DuplicateIndex && len(a.Entries) >= 3 {
			// Insert a duplicate of the first row; reconcile must
			// quarantine it as an index_duplicate, not use it twice.
			dup := a.Entries[0]
			a.Entries = append(a.Entries, index.Entry{})
			copy(a.Entries[2:], a.Entries[1:])
			a.Entries[1] = dup
			c.Duplicate++
		}
	}
	return c
}

// shiftPTS adds delta to every in-stream meta PTS and every index
// entry PTS without touching byte offsets, turning a representation
// into a time-skewed layer whose framing stays byte-identical. The MDIQ
// magic contains no zero byte, so it survives emulation-prevention
// escaping unchanged and can be located inside each scanned unit.
func shiftPTS(a *Asset, delta timeline.Ticks) {
	scan, err := bitstream.Scan(a.Spec.Codec, a.Data, bitstream.DefaultLimits())
	if err != nil {
		return // leave PTS untouched; align will simply see no skew
	}
	magic := []byte("MDIQ")
	for _, u := range scan.Units {
		span := a.Data[u.Offset : u.Offset+u.Length]
		idx := bytes.Index(span, magic)
		if idx < 0 {
			continue
		}
		off := u.Offset + int64(idx)
		cur := binary.BigEndian.Uint64(a.Data[off+8 : off+16])
		binary.BigEndian.PutUint64(a.Data[off+8:off+16], cur+uint64(delta))
	}
	for i := range a.Entries {
		a.Entries[i].PTS += delta
	}
}

// unitFramingLen returns the bytes preceding the MDIQ meta header for
// a non-escaped unit, used by corruption injection which needs the
// payload region before any escaping side effects are possible.
func unitFramingLen(codec bitstream.Codec) int {
	switch codec {
	case bitstream.CodecAV1:
		return 2 // OBU header + 1-byte LEB128
	case bitstream.CodecH265:
		return 6 // 4-byte start code + 2 NAL header bytes
	default:
		return 5 // 4-byte start code + 1 NAL header byte
	}
}

// FaultCounts mirrors report.InjectionCounts without an import cycle.
type FaultCounts struct {
	Truncated    int
	Corrupt      int
	MissingIndex int
	Duplicate    int
	Misaligned   int
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
