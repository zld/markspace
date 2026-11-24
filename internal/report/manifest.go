package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"mediapipe/internal/model"
	"mediapipe/internal/segment"
)

// Summary contains every scalar acceptance field. It is separately hashed so
// verify can prove the terminal summary and manifest describe the same result.
type Summary struct {
	BitrateTargetBps     int     `json:"bitrate_target_bps"`
	BitrateActualBps     float64 `json:"bitrate_actual_bps"`
	BitrateDeviationPct  float64 `json:"bitrate_deviation_pct"`
	PacketLost           int64   `json:"packet_lost"`
	PacketDuplicate      int64   `json:"packet_duplicate"`
	PacketLate           int64   `json:"packet_late"`
	PacketCorrupt        int64   `json:"packet_corrupt"`
	RTCPInvalid          int64   `json:"rtcp_invalid,omitempty"`
	RTCPStaleReports     int64   `json:"rtcp_stale_reports,omitempty"`
	RTCPMissing          bool    `json:"rtcp_missing,omitempty"`
	SilenceWindows       int     `json:"silence_windows"`
	UnknownWindows       int     `json:"unknown_windows"`
	VoicedWindows        int     `json:"voiced_windows,omitempty"`
	DeduplicatedSegments int     `json:"deduplicated_segments"`
	ManifestStartMs      int64   `json:"manifest_start_ms"`
	ManifestEndMs        int64   `json:"manifest_end_ms"`
	MaxAlignmentErrorMs  int64   `json:"max_alignment_error_ms"`
	ResumedFromPacket    int64   `json:"resumed_from_packet,omitempty"`
	CheckpointPacket     int64   `json:"checkpoint_packet,omitempty"`
	Truncated            bool    `json:"truncated"`
}

func (s Summary) Digest() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Manifest is the complete deterministic distribution manifest.
type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	// Anonymous embedding promotes all acceptance counters to top-level keys.
	Summary
	SummarySHA256     string                    `json:"summary_sha256"`
	InputPath         string                    `json:"input_path"`
	InputSHA256       string                    `json:"input_sha256"`
	InputBytes        int64                     `json:"input_bytes"`
	SegmentMs         int64                     `json:"segment_ms"`
	CheckpointPackets int                       `json:"checkpoint_packets"`
	ReorderWindow     int                       `json:"reorder_window"`
	Segments          []model.Segment           `json:"segments"`
	Issues            []model.Error             `json:"issues,omitempty"`
	Windows           []model.SilenceWindow     `json:"windows,omitempty"`
	Fingerprints      []model.WindowFingerprint `json:"fingerprints,omitempty"`
	DedupeMappings    []model.DedupeMapping     `json:"dedupe_mappings,omitempty"`
}

func (m *Manifest) Prepare() {
	if len(m.Segments) > 0 {
		m.Summary.ManifestStartMs = m.Segments[0].StartMs
		m.Summary.ManifestEndMs = m.Segments[len(m.Segments)-1].EndMs
	}
	var maxAlign int64
	var payloadBytes int64
	var durationMs int64
	for _, s := range m.Segments {
		if s.BoundaryAlignmentMs > maxAlign {
			maxAlign = s.BoundaryAlignmentMs
		}
		if s.StartAlignmentMs > maxAlign {
			maxAlign = s.StartAlignmentMs
		}
		payloadBytes += s.PayloadBytes
		if s.EndMs > s.StartMs {
			durationMs += s.EndMs - s.StartMs
		}
	}
	m.Summary.MaxAlignmentErrorMs = maxAlign
	if durationMs > 0 {
		m.Summary.BitrateActualBps = round2(float64(payloadBytes) * 8 * 1000 / float64(durationMs))
	}
	if m.Summary.BitrateTargetBps > 0 {
		deviation := math.Abs(m.Summary.BitrateActualBps-float64(m.Summary.BitrateTargetBps)) /
			float64(m.Summary.BitrateTargetBps) * 100
		m.Summary.BitrateDeviationPct = round2(deviation)
	}
	if len(m.Windows) > 0 {
		var silent, unknown, voiced int
		for _, w := range m.Windows {
			switch w.State {
			case model.WindowSilent:
				silent++
			case model.WindowUnknown:
				unknown++
			case model.WindowVoiced:
				voiced++
			}
		}
		m.Summary.SilenceWindows = silent
		m.Summary.UnknownWindows = unknown
		m.Summary.VoicedWindows = voiced
	}
	m.Summary.DeduplicatedSegments = len(m.DedupeMappings)
	m.SummarySHA256 = m.Summary.Digest()
}

func WriteManifest(dir string, m *Manifest) (string, error) {
	m.Prepare()
	path := filepath.Join(dir, "manifest.json")
	if err := segment.WriteJSON(path, m); err != nil {
		return "", err
	}
	summaryPath := filepath.Join(dir, "summary.json")
	if err := segment.WriteJSON(summaryPath, m.Summary); err != nil {
		return "", err
	}
	return path, nil
}

func LoadManifest(path string) (*Manifest, error) {
	var m Manifest
	if err := segment.ReadJSON(path, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// PrintSummary returns the stable indented JSON summary used by the terminal.
func PrintSummary(s Summary) string {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

func FileSHA256(path string) (string, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fi.Size(), err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), fi.Size(), nil
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
