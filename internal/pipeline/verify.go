package pipeline

import (
	"os"
	"path/filepath"

	"mediarun/internal/fault"
	"mediarun/internal/index"
	"mediarun/internal/provenance"
)

// VerifyReport is the independently recomputed acceptance evidence for a run.
// Everything here is derived from files on disk, not from in-memory state, so a
// resumed or repeated run can be compared against the first.
type VerifyReport struct {
	SegmentsChecked int
	RoundtripHits   int
	RoundtripTotal  int
	KeyframeHits    int
	KeyframeTotal   int
	SegmentsEqual   bool
	Idempotent      bool
	Misses          []string
	// Hashes are the segment digests observed by this verification.
	Hashes []string
	// BaselineHashes are the digests a previous run recorded, when available.
	BaselineHashes []string
}

// VerifiedSegments reports whether every segment hash matched the manifest and
// every index resolved. It is the strict form of SegmentsEqual, which only
// compares digests.
func (v *VerifyReport) VerifiedSegments() bool {
	return v.SegmentsEqual && len(v.Misses) == 0 &&
		v.RoundtripTotal > 0 && v.RoundtripHits == v.RoundtripTotal
}

// RoundtripRatio returns the percentage of samples that round-tripped.
func (v *VerifyReport) RoundtripRatio() int {
	if v.RoundtripTotal == 0 {
		return 100
	}
	return v.RoundtripHits * 100 / v.RoundtripTotal
}

// KeyframeRatio returns the percentage of keyframes that resolved.
func (v *VerifyReport) KeyframeRatio() int {
	if v.KeyframeTotal == 0 {
		return 100
	}
	return v.KeyframeHits * 100 / v.KeyframeTotal
}

// Verify re-reads every segment and its index from disk and recomputes the
// hashes, keyframe hits and sample roundtrip. When a previous manifest exists
// it is used as the idempotency baseline.
func Verify(dir string, m *provenance.Manifest) (*VerifyReport, error) {
	if m == nil {
		return nil, fault.New(fault.StagePipeline, fault.KindConfig, "verify needs a manifest")
	}
	rep := &VerifyReport{}
	for _, seg := range m.Segments {
		segPath := filepath.Join(dir, filepath.FromSlash(seg.Path))
		sum, n, err := provenance.HashFile(segPath)
		if err != nil {
			return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "hash %s", segPath)
		}
		rep.SegmentsChecked++
		rep.Hashes = append(rep.Hashes, sum)
		if n != seg.Bytes {
			rep.Misses = append(rep.Misses, seg.Path+" size changed")
			continue
		}
		if sum != seg.SHA256 {
			rep.Misses = append(rep.Misses, seg.Path+" digest changed")
			continue
		}
		idxf, err := readIndexFile(dir, seg.Index)
		if err != nil {
			return nil, err
		}
		kr, err := index.Verify(segPath, idxf)
		if err != nil {
			return nil, err
		}
		rep.KeyframeHits += kr.Hits
		rep.KeyframeTotal += kr.Checked
		rep.Misses = append(rep.Misses, kr.Misses...)
		sr, err := index.VerifySamples(segPath, idxf)
		if err != nil {
			return nil, err
		}
		rep.RoundtripHits += sr.Hits
		rep.RoundtripTotal += sr.Checked
		rep.Misses = append(rep.Misses, sr.Misses...)
	}

	// Segment digests on disk must match what the manifest recorded for this
	// run; otherwise the manifest is stale.
	rep.SegmentsEqual = provenance.EqualHashes(rep.Hashes, m.SegmentHashes())
	// Idempotency is only meaningful against a previous run in the same
	// directory. Without one, the first run defines the baseline.
	rep.Idempotent = rep.SegmentsEqual && len(rep.Misses) == 0
	rep.BaselineHashes = m.SegmentHashes()
	return rep, nil
}

// ApplyBaseline folds a previous run's manifest into the report. When a
// baseline exists, idempotency means the new segment digests equal the old
// ones, not merely that the manifest agrees with the files it just wrote.
func ApplyBaseline(rep *VerifyReport, previous *provenance.Manifest) {
	if rep == nil || previous == nil {
		return
	}
	prior := previous.SegmentHashes()
	rep.BaselineHashes = prior
	rep.Idempotent = len(prior) == len(rep.Hashes) && provenance.EqualHashes(rep.Hashes, prior) && len(rep.Misses) == 0
}

// verifyCachedManifest loads the manifest written by a previous run so a
// resumed run can report idempotency even before rewriting the manifest.
func verifyCachedManifest(dir string) (*provenance.Manifest, bool) {
	path := filepath.Join(dir, ManifestName)
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	m, err := provenance.Load(path)
	if err != nil {
		return nil, false
	}
	return m, true
}
