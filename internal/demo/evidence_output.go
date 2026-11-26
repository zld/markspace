package demo

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediapipeline/internal/index"
	"mediapipeline/internal/rtphint"
)

type evidenceFile struct {
	Work              string                 `json:"work"`
	ProductKey        string                 `json:"product_key"`
	ProductPath       string                 `json:"product_path"`
	OutputSHA256      string                 `json:"output_sha256"`
	ReusedProduct     bool                   `json:"reused_product"`
	PipePeakChunks    int64                  `json:"pipe_peak_chunks"`
	Verification      verificationEvidence   `json:"verification"`
	ContainerDecision index.Decision         `json:"container_decision"`
	RTP               rtphint.SequenceReport `json:"rtp"`
	CorruptInputs     bool                   `json:"corrupt_inputs"`
}

type verificationEvidence struct {
	RoundtripRate   float64 `json:"roundtrip_rate"`
	KeyframeHitRate float64 `json:"keyframe_hit_rate"`
	SegmentHashRate float64 `json:"segment_hash_rate"`
	Samples         int     `json:"samples"`
	Segments        int     `json:"segments"`
}

func writeEvidence(path string, cfg Config, r *PublishResult, rtp rtphint.SequenceReport, d index.Decision, corrupt bool) error {
	ev := evidenceFile{
		Work:              cfg.Work,
		ContainerDecision: d,
		RTP:               rtp,
		CorruptInputs:     corrupt,
	}
	if r != nil {
		ev.ProductKey = r.Key
		ev.ProductPath = r.ProductPath
		ev.OutputSHA256 = r.OutputSHA
		ev.ReusedProduct = r.Reused
		ev.PipePeakChunks = r.PipeStats.PeakChunks
		ev.Verification = verificationEvidence{
			RoundtripRate:   r.Verify.RoundtripRate,
			KeyframeHitRate: r.Verify.KeyframeHitRate,
			SegmentHashRate: r.Verify.SegmentHashRate,
			Samples:         r.Verify.Samples,
			Segments:        r.Verify.Segments,
		}
	}
	b, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
