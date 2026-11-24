package report

import (
	"fmt"
	"path/filepath"

	"mediapipe/internal/segment"
)

type Verification struct {
	OK                            bool     `json:"ok"`
	SummaryMatch                  bool     `json:"summary_match"`
	SummaryConsistent             bool     `json:"summary_consistent"`
	IndexReadable                 bool     `json:"index_readable"`
	SegmentCountManifest          int      `json:"segment_count_manifest"`
	SegmentCountIndex             int      `json:"segment_count_index"`
	ManifestIndexAlignmentErrorMs int64    `json:"manifest_index_alignment_error_ms"`
	MaxAllowedAlignmentErrorMs    int64    `json:"max_allowed_alignment_error_ms"`
	Differences                   []string `json:"differences,omitempty"`
}

// VerifyOutput compares manifest summary artifacts with the MP4 segment index.
func VerifyOutput(outDir string) (*Verification, error) {
	v := &Verification{MaxAllowedAlignmentErrorMs: 20}
	manifestPath := filepath.Join(outDir, "manifest.json")
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return v, fmt.Errorf("load manifest: %w", err)
	}

	var summaryOnDisk Summary
	if err := segment.ReadJSON(filepath.Join(outDir, "summary.json"), &summaryOnDisk); err != nil {
		v.Differences = append(v.Differences, "summary.json unreadable: "+err.Error())
	} else {
		diskDigest := summaryOnDisk.Digest()
		manifestDigest := m.Summary.Digest()
		v.SummaryMatch = diskDigest == manifestDigest && m.SummarySHA256 == manifestDigest
		v.SummaryConsistent = v.SummaryMatch
		if !v.SummaryMatch {
			v.Differences = append(v.Differences, "summary digest mismatch")
		}
	}

	idx, err := segment.ReadMP4Index(filepath.Join(outDir, "index.mp4"))
	if err != nil {
		v.Differences = append(v.Differences, "index unreadable: "+err.Error())
	} else {
		v.IndexReadable = true
		v.SegmentCountIndex = len(idx.Segments)
	}
	v.SegmentCountManifest = len(m.Segments)

	if v.IndexReadable {
		if len(idx.Segments) != len(m.Segments) {
			v.Differences = append(v.Differences, fmt.Sprintf(
				"segment count mismatch manifest=%d index=%d", len(m.Segments), len(idx.Segments)))
		}
		n := len(m.Segments)
		if len(idx.Segments) < n {
			n = len(idx.Segments)
		}
		for i := 0; i < n; i++ {
			if d := abs64(m.Segments[i].StartMs - idx.Segments[i].StartMs); d > v.ManifestIndexAlignmentErrorMs {
				v.ManifestIndexAlignmentErrorMs = d
			}
			if d := abs64(m.Segments[i].EndMs - idx.Segments[i].EndMs); d > v.ManifestIndexAlignmentErrorMs {
				v.ManifestIndexAlignmentErrorMs = d
			}
		}
		if v.ManifestIndexAlignmentErrorMs > v.MaxAllowedAlignmentErrorMs {
			v.Differences = append(v.Differences, "manifest/index alignment exceeds tolerance")
		}
	}

	v.OK = v.SummaryConsistent && v.IndexReadable && len(v.Differences) == 0
	return v, nil
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
