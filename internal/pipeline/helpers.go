package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	pwrtp "pwflow/internal/rtp"
)

// WriteManifest atomically writes deterministic indented JSON.
func WriteManifest(path string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func checkpointSummary(m *Manifest, bundle *inputBundle, segments []*SegmentResult, audio *audioAnalysis) ManifestSummary {
	if m != nil {
		return ManifestSummary{
			Status: m.Status, SegmentCount: m.Counts.Segments,
			SilenceCount:     m.Counts.SilenceIntervals,
			DuplicatePackets: m.Counts.DuplicatePackets,
			MissingPackets:   m.Counts.MissingPackets,
		}
	}
	completed := 0
	for _, s := range segments {
		if s != nil && s.FileSHA256 != "" {
			completed++
		}
	}
	status := StatusPass
	if len(bundle.ebml.Damages) > 0 || bundle.repaired.Missing > 0 ||
		len(bundle.parseErrors) > 0 || hasUnmappedGap(bundle.repaired.Gaps) {
		status = StatusGaps
	}
	silences := 0
	if audio != nil {
		silences = len(audio.silence.Silences)
	}
	return ManifestSummary{
		Status: status, SegmentCount: completed, SilenceCount: silences,
		DuplicatePackets: bundle.repaired.Duplicates,
		MissingPackets:   bundle.repaired.Missing,
	}
}

func hasUnmappedGap(gaps []pwrtp.Gap) bool {
	for _, g := range gaps {
		if !g.Mapped {
			return true
		}
	}
	return false
}
