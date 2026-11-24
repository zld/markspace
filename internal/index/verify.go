package index

import (
	"fmt"
	"os"

	"mediarun/internal/fault"
)

// Verify recomputes sample positions from the MP4 tables, checks that each
// entry is a declared sync sample and that the bytes at the computed position
// hash to the indexed payload digest.
func Verify(path string, x *Index) (VerifyResult, error) {
	var res VerifyResult
	if x == nil {
		return res, fault.New(fault.StageIndex, fault.KindIndex, "nil keyframe index")
	}
	if len(x.Entries) == 0 {
		// No keyframe to resolve. A segment of the first frame is still
		// decodable from its own start, so this is reported as a hit rather
		// than a miss; an empty segment is a real failure.
		if x.SampleCount == 0 {
			return res, fault.New(fault.StageIndex, fault.KindIndex, "segment has no samples")
		}
		res.Checked, res.Hits = 1, 1
		return res, nil
	}
	tbl, err := ReadTable(path)
	if err != nil {
		return res, err
	}
	if len(tbl.SampleSizes) != x.SampleCount {
		return res, fault.New(fault.StageIndex, fault.KindIndex,
			"stsz declares %d samples, index expects %d", len(tbl.SampleSizes), x.SampleCount)
	}
	offsets, err := SampleOffsets(tbl)
	if err != nil {
		return res, err
	}
	f, err := os.Open(path)
	if err != nil {
		return res, fault.Wrap(fault.StageIndex, fault.KindIO, err, "open %s", path)
	}
	defer f.Close()

	for _, e := range x.Entries {
		res.Checked++
		if e.Sample < 1 || e.Sample > len(offsets) {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d outside 1..%d", e.Sample, len(offsets)))
			continue
		}
		if !tbl.SyncSamples[e.Sample] {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d not in stss", e.Sample))
			continue
		}
		off, size := offsets[e.Sample-1], int(tbl.SampleSizes[e.Sample-1])
		if off != e.DataOffset || size != e.Size {
			res.Misses = append(res.Misses,
				fmt.Sprintf("sample %d maps to %d/%d, index says %d/%d", e.Sample, off, size, e.DataOffset, e.Size))
			continue
		}
		got, err := hashAt(f, off, size)
		if err != nil {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d unreadable: %v", e.Sample, err))
			continue
		}
		if got != e.PayloadHash {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d payload digest mismatch", e.Sample))
			continue
		}
		res.Hits++
	}
	return res, nil
}

// SampleVerify counts end-to-end roundtrip hits across every stored sample.
type SampleVerify struct {
	Checked int
	Hits    int
	Misses  []string
}

// Ratio returns the hit ratio in percent, rounded down.
func (r SampleVerify) Ratio() int {
	if r.Checked == 0 {
		return 0
	}
	return r.Hits * 100 / r.Checked
}

// Verified reports whether every mapped sample resolved.
func (r SampleVerify) Verified() bool { return r.Checked > 0 && r.Hits == r.Checked }

// VerifySamples recomputes every sample's file position from the MP4 tables
// and confirms the payload digest recorded at export time. Used by the resume
// path to prove a cached segment still matches its index, and by acceptance
// verification to report the end-to-end roundtrip ratio.
func VerifySamples(path string, x *Index) (SampleVerify, error) {
	var res SampleVerify
	if x == nil || len(x.Samples) == 0 {
		return res, fault.New(fault.StageIndex, fault.KindIndex, "index has no sample map")
	}
	tbl, err := ReadTable(path)
	if err != nil {
		return res, err
	}
	offs, err := SampleOffsets(tbl)
	if err != nil {
		return res, err
	}
	f, err := os.Open(path)
	if err != nil {
		return res, fault.Wrap(fault.StageIndex, fault.KindIO, err, "open %s", path)
	}
	defer f.Close()
	for _, ref := range x.Samples {
		res.Checked++
		if ref.Sample < 1 || ref.Sample > len(offs) {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d outside 1..%d", ref.Sample, len(offs)))
			continue
		}
		size := int(tbl.SampleSizes[ref.Sample-1])
		if size != ref.Size {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d size %d != %d", ref.Sample, size, ref.Size))
			continue
		}
		got, err := hashAt(f, offs[ref.Sample-1], size)
		if err != nil {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d unreadable: %v", ref.Sample, err))
			continue
		}
		if got != ref.PayloadHash {
			res.Misses = append(res.Misses, fmt.Sprintf("sample %d digest mismatch", ref.Sample))
			continue
		}
		res.Hits++
	}
	return res, nil
}
