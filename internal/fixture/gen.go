package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// CorruptTruth records the planted truncation for offset accuracy checks.
type CorruptTruth struct {
	Manifest           string `json:"manifest"`
	Segment            string `json:"segment"`
	CutOffsetBytes     int64  `json:"cut_offset_bytes"`
	OriginalSizeBytes  int    `json:"original_size_bytes"`
	VictimSampleIndex  int    `json:"victim_sample_index"`
	SampleStartOffset  int64  `json:"sample_start_offset"`
	Timescale          uint32 `json:"timescale"`
	SampleTimeUnified  int64  `json:"sample_time_unified_ticks"`
}

// GroundTruth describes every generated scenario and the planted fault.
type GroundTruth struct {
	OutDir                  string       `json:"out_dir"`
	GoodManifest            string       `json:"good_manifest"`
	CorruptManifest         string       `json:"corrupt_manifest"`
	DiscontinuityManifest   string       `json:"discontinuity_manifest"`
	VFRManifest             string       `json:"vfr_manifest"`
	WrapDriftManifest       string       `json:"wrap_drift_manifest"`
	DashManifest            string       `json:"dash_manifest"`
	DashMisalignedManifest  string       `json:"dash_misaligned_manifest"`
	Corrupt                 CorruptTruth `json:"corrupt_truth"`
}

// Generate creates the full deterministic fixture tree under dir.
func Generate(dir string) (*GroundTruth, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	gt := &GroundTruth{OutDir: dir}

	if err := genGood(dir, gt); err != nil {
		return nil, err
	}
	if err := genCorrupt(dir, gt); err != nil {
		return nil, err
	}
	if err := genDiscontinuity(dir, gt); err != nil {
		return nil, err
	}
	if err := genVFR(dir, gt); err != nil {
		return nil, err
	}
	if err := genWrapDrift(dir, gt); err != nil {
		return nil, err
	}
	if err := genDASH(dir, gt); err != nil {
		return nil, err
	}

	b, err := json.MarshalIndent(gt, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "ground_truth.json"), b, 0o644); err != nil {
		return nil, err
	}
	return gt, nil
}

func writeBytes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeProgressive(path string, ts uint32, deltas []uint32, size int, gop int, seed uint64) (*BuiltMedia, error) {
	spec := ProgressiveSpec{Timescale: ts, Seed: seed}
	for i, d := range deltas {
		spec.Samples = append(spec.Samples, SampleSpec{
			Delta: d,
			Size:  size + (i%5)*128,
			Sync:  gop > 0 && i%gop == 0,
		})
	}
	built, err := BuildProgressive(spec)
	if err != nil {
		return nil, err
	}
	if err := writeBytes(path, built.Bytes); err != nil {
		return nil, err
	}
	return built, nil
}
