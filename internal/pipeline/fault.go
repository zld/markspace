package pipeline

import (
	"bytes"
	"encoding/binary"
	"math/rand"

	"mediq/internal/bitstream"
	"mediq/internal/index"
	"mediq/internal/timeline"
)

// FaultCounts mirrors report.InjectionCounts without an import cycle.
type FaultCounts struct {
	Truncated    int
	Corrupt      int
	MissingIndex int
	Duplicate    int
	Misaligned   int
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
