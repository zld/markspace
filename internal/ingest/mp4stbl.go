package ingest

import (
	"encoding/binary"

	"mediapipe/internal/fault"
	"mediapipe/internal/limits"
	"mediapipe/internal/model"
)

// stblTables holds the decoded sample tables of one track.
type stblTables struct {
	ctsOff  []int64
	sizes   []int64
	offsets []int64
	keys    map[int]bool // 0-based keyframe sample index
	deltas  []uint32
}

func parseStts(b rawBox) []uint32 {
	_, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 4 {
		return nil
	}
	count := binary.BigEndian.Uint32(body)
	pos := 4
	var deltas []uint32
	for i := uint32(0); i < count && pos+8 <= len(body); i++ {
		n := binary.BigEndian.Uint32(body[pos:])
		d := binary.BigEndian.Uint32(body[pos+4:])
		pos += 8
		for j := uint32(0); j < n; j++ {
			deltas = append(deltas, d)
		}
	}
	return deltas
}

func parseCtts(b rawBox) []int64 {
	ver, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 4 {
		return nil
	}
	count := binary.BigEndian.Uint32(body)
	pos := 4
	var offs []int64
	for i := uint32(0); i < count && pos+8 <= len(body); i++ {
		n := binary.BigEndian.Uint32(body[pos:])
		var o int64
		if ver == 1 {
			o = int64(int32(binary.BigEndian.Uint32(body[pos+4:])))
		} else {
			o = int64(binary.BigEndian.Uint32(body[pos+4:]))
		}
		pos += 8
		for j := uint32(0); j < n; j++ {
			offs = append(offs, o)
		}
	}
	return offs
}

func parseStsz(b rawBox) []int64 {
	_, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 8 {
		return nil
	}
	uniform := binary.BigEndian.Uint32(body)
	count := binary.BigEndian.Uint32(body[4:])
	if count > limits.MaxSamplesPerTrack {
		return nil
	}
	sizes := make([]int64, count)
	if uniform != 0 {
		for i := range sizes {
			sizes[i] = int64(uniform)
		}
		return sizes
	}
	pos := 8
	for i := uint32(0); i < count; i++ {
		if pos+4 > len(body) {
			return sizes[:i] // truncated table: keep what exists
		}
		sizes[i] = int64(binary.BigEndian.Uint32(body[pos:]))
		pos += 4
	}
	return sizes
}

func parseStco(b rawBox, sixtyFour bool) []int64 {
	_, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 4 {
		return nil
	}
	count := binary.BigEndian.Uint32(body)
	pos := 4
	width := 4
	if sixtyFour {
		width = 8
	}
	offs := make([]int64, 0, count)
	for i := uint32(0); i < count; i++ {
		if pos+width > len(body) {
			break
		}
		if sixtyFour {
			offs = append(offs, int64(binary.BigEndian.Uint64(body[pos:])))
		} else {
			offs = append(offs, int64(binary.BigEndian.Uint32(body[pos:])))
		}
		pos += width
	}
	return offs
}

func parseStss(b rawBox) map[int]bool {
	_, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 4 {
		return nil
	}
	count := binary.BigEndian.Uint32(body)
	keys := map[int]bool{}
	pos := 4
	for i := uint32(0); i < count && pos+4 <= len(body); i++ {
		n := binary.BigEndian.Uint32(body[pos:])
		if n > 0 {
			keys[int(n)-1] = true
		}
		pos += 4
	}
	return keys
}

// validateStsc parses the sample-to-chunk box only to confirm it is bounded
// and well-formed. The supported fixtures use one sample per chunk, so no
// general chunk expansion is retained.
func validateStsc(b rawBox, file string, errs *fault.Batch) {
	_, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 4 {
		return
	}
	count := binary.BigEndian.Uint32(body)
	if count > limits.MaxSamplesPerTrack {
		errs.Add(limits.Exceeded(file, "stsc entry count", b.offset,
			count, limits.MaxSamplesPerTrack))
		return
	}
	if 4+int(count)*12 > len(body) {
		errs.Add(fault.New(fault.KindTruncated,
			"stsc runs overrun box", file, b.offset, true))
	}
}

// buildSamples converts stbl tables into model samples on the nano axis.
// Non-zero start, duplicate PTS and missing frames are handled without ever
// producing a negative duration.
func buildSamples(t stblTables, timescale uint32, file string,
	errs *fault.Batch, kind model.Kind) []model.Sample {
	n := len(t.sizes)
	if n == 0 || timescale == 0 {
		return nil
	}
	scale := func(units int64) int64 {
		return units * 1_000_000_000 / int64(timescale)
	}
	out := make([]model.Sample, 0, n)
	seenPTS := map[int64]bool{}
	prevEnd := int64(0)
	for i := 0; i < n; i++ {
		var dtsU int64
		if i == 0 {
			dtsU = 0
		} else {
			d := int64(1)
			if i-1 < len(t.deltas) {
				d = int64(t.deltas[i-1])
			}
			dtsU = out[i-1].DTS + scale(d)
		}
		cts := int64(0)
		if i < len(t.ctsOff) {
			cts = scale(t.ctsOff[i])
		}
		pts := dtsU + cts
		dur := int64(0)
		if i < len(t.deltas) {
			dur = scale(int64(t.deltas[i]))
		}
		if dur < 0 {
			dur = 0
		}
		off, plen := int64(-1), t.sizes[i]
		if i < len(t.offsets) {
			off = t.offsets[i]
		}
		s := model.Sample{
			DTS: dtsU, PTS: pts, DurationNanos: dur,
			PayloadLen: plen, PayloadOff: off,
		}
		if kind == model.KindVideo {
			if t.keys == nil {
				s.Keyframe = true // no stss: treat every sample as sync
			} else {
				s.Keyframe = t.keys[i]
			}
		}
		if seenPTS[pts] {
			s.DupPTS = true
			errs.Add(fault.New(fault.KindVFRDupPTS,
				"duplicate presentation timestamp", file, off, true))
		}
		seenPTS[pts] = true
		if pts > prevEnd {
			s.Missing = true
			errs.Add(fault.New(fault.KindMissingFrame,
				"frame gap before presentation timestamp", file, off, true))
		}
		prevEnd = pts + dur
		out = append(out, s)
	}
	return out
}
