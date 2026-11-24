package scan

import (
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"archive/internal/model"
)

// finishTrak assembles the sample table of the current track and validates
// every sample's essence packet against its CRC.
func (s *mp4Scanner) finishTrak() {
	t := s.trak
	s.trak = nil
	if t == nil {
		return
	}
	t.meta.Samples = buildSamples(t.stts, t.stsc, t.sizes, t.chunks)
	s.validateSamples(&t.meta)
	s.res.Tracks = append(s.res.Tracks, t.meta)
}

// buildSamples expands stts/stsc/stsz/stco into per-sample references with
// decode-time timestamps (no reordering in archival sources we handle).
func buildSamples(stts []mp4.SttsEntry, stsc []mp4.StscEntry, sizes []uint32, chunks []uint64) []SampleRef {
	n := len(sizes)
	if n == 0 {
		return nil
	}
	deltas := make([]uint32, 0, n)
	for _, e := range stts {
		for i := uint32(0); i < e.SampleCount && len(deltas) < n; i++ {
			deltas = append(deltas, e.SampleDelta)
		}
	}
	for len(deltas) < n { // stts shorter than stsz: repeat last delta
		var last uint32
		if len(deltas) > 0 {
			last = deltas[len(deltas)-1]
		}
		deltas = append(deltas, last)
	}
	out := make([]SampleRef, 0, n)
	var dts int64
	idx := 0
	for ci, coff := range chunks {
		spc := samplesPerChunk(stsc, uint32(ci)+1)
		off := int64(coff)
		for k := uint32(0); k < spc && idx < n; k++ {
			out = append(out, SampleRef{Offset: off, Size: int64(sizes[idx]), PTS: dts})
			off += int64(sizes[idx])
			dts += int64(deltas[idx])
			idx++
		}
	}
	return out
}

func samplesPerChunk(stsc []mp4.StscEntry, chunk uint32) uint32 {
	var spc uint32 = 1
	for _, e := range stsc {
		if e.FirstChunk <= chunk {
			spc = e.SamplesPerChunk
		} else {
			break
		}
	}
	return spc
}

// validateSamples checks each sample in place. A truncated sample marks
// itself and everything after it; a CRC failure marks only that sample, so
// later packets stay recoverable.
func (s *mp4Scanner) validateSamples(t *TrackMeta) {
	truncated := false
	for i := range t.Samples {
		smp := &t.Samples[i]
		if truncated {
			smp.Bad = true
			continue
		}
		if smp.Offset < 0 || smp.Size <= 0 || smp.Offset+smp.Size > s.size {
			s.err(model.ErrTruncated, smp.Offset, t.ID, "sample data cut off")
			smp.Bad = true
			truncated = true
			continue
		}
		buf := make([]byte, smp.Size)
		if _, err := s.f.ReadAt(buf, smp.Offset); err != nil && err != io.EOF {
			smp.Bad = true
			continue
		}
		if !model.ValidateEssence(buf) {
			s.err(model.ErrEssenceCRC, smp.Offset, t.ID, "essence integrity check failed")
			smp.Bad = true
		}
	}
}

// resync scans forward for the next plausible box header after a corrupt
// size field. Returns -1 when nothing recognizable remains.
func (s *mp4Scanner) resync(from, end int64) int64 {
	known := []string{"ftyp", "moov", "mdat", "free", "skip", "moof", "sidx"}
	const chunk = 64 << 10
	buf := make([]byte, chunk+8)
	for base := from; base < end; base += chunk {
		n := int64(len(buf))
		if base+n > end {
			n = end - base
		}
		if n <= 8 {
			return -1
		}
		if _, err := s.f.ReadAt(buf[:n], base); err != nil && err != io.EOF {
			return -1
		}
		for i := int64(0); i+8 <= n; i++ {
			typ := string(buf[i+4 : i+8])
			for _, k := range known {
				if typ == k {
					return base + i
				}
			}
		}
	}
	return -1
}

// rotationFromMatrix decodes the tkhd 3x3 fixed-point matrix into degrees.
// The original matrix is never modified; unknown matrices report no rotation.
func rotationFromMatrix(m [9]int32) (int, bool) {
	const one = 0x10000
	switch {
	case m[0] == one && m[4] == one:
		return 0, false
	case m[1] == one && m[3] == -one:
		return 90, true
	case m[0] == -one && m[4] == -one:
		return 180, true
	case m[1] == -one && m[3] == one:
		return 270, true
	}
	return 0, false
}

// ensure os import is used even if future edits drop ReadAt calls
var _ = os.O_RDONLY
