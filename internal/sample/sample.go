// Package sample generates the demo fixtures: MP4 and MXF files with known
// rotation/crop/colour metadata and self-checking essence packets, plus
// controlled corruption and truncation. No external media is ever used.
package sample

import (
	"os"
	"sort"
)

// SampleInfo records where one generated sample landed in the file, so the
// demo can compare detected damage offsets against ground truth.
type SampleInfo struct {
	Track  int
	Offset int64
	Size   int64
}

// Info describes a generated file.
type Info struct {
	Samples    []SampleInfo
	CorruptAt  int64
	TruncateAt int64
}

// PacketSizeAt returns the size of the sample containing byte offset off
// (0 when off is outside all samples).
func (i *Info) PacketSizeAt(off int64) int64 {
	for _, s := range i.Samples {
		if off >= s.Offset && off < s.Offset+s.Size {
			return s.Size
		}
	}
	return 0
}

// applyDamage flips one byte at CorruptAt and cuts the file at TruncateAt.
func applyDamage(path string, corruptAt, truncateAt int64) error {
	if corruptAt >= 0 {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		var b [1]byte
		if _, err := f.ReadAt(b[:], corruptAt); err == nil {
			b[0] ^= 0xFF
			_, err = f.WriteAt(b[:], corruptAt)
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	if truncateAt >= 0 {
		if err := os.Truncate(path, truncateAt); err != nil {
			return err
		}
	}
	return nil
}

// mergedSample describes one sample before offsets are assigned.
type mergedSample struct {
	track int
	ptsNs int64
	data  []byte
}

// sortMerged orders samples by (PTS, track) — the same order the demuxer
// and the MP4 interleave use, keeping everything deterministic.
func sortMerged(ms []mergedSample) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].ptsNs != ms[j].ptsNs {
			return ms[i].ptsNs < ms[j].ptsNs
		}
		return ms[i].track < ms[j].track
	})
}
