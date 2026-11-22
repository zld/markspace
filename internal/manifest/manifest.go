// Package manifest builds and validates the verifiable merge manifest:
// target vs. actual bitrate, quality statistics, time ranges and hashes.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Range is a half-open tick interval on the manifest timescale.
type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Segment carries per-segment merge statistics.
type Segment struct {
	Index           int   `json:"index"`
	Start           int64 `json:"start"` // ticks
	End             int64 `json:"end"`   // ticks
	TargetBps       int64 `json:"target_bps"`
	ActualBps       int64 `json:"actual_bps"`
	GapPackets      int   `json:"gap_packets"`
	CompleteSamples int   `json:"complete_samples"`
	TotalSamples    int   `json:"total_samples"`
}

// Quality aggregates stream quality counters.
type Quality struct {
	PacketLoss      int `json:"packet_loss"`
	RecoveredPacket int `json:"recovered_packets"`
	Duplicates      int `json:"duplicates"`
	GapCount        int `json:"gap_count"`
}

// Manifest is the committed, verifiable summary of one merge run.
type Manifest struct {
	Version          int               `json:"version"`
	Timescale        int64             `json:"timescale"` // manifest tick rate
	TargetBps        int64             `json:"target_bps"`
	ActualBps        int64             `json:"actual_bps"`
	AVSkewNum        int64             `json:"av_skew_num"` // skew as rational num/den seconds
	AVSkewDen        int64             `json:"av_skew_den"`
	CommittedRanges  []Range           `json:"committed_ranges"`
	Segments         []Segment         `json:"segments"`
	Quality          Quality           `json:"quality"`
	Hashes           map[string]string `json:"hashes"` // artifact name -> sha256
	ProvenanceSHA256 string            `json:"provenance_sha256"`
	ContentSHA256    string            `json:"content_sha256"`
}

// BitrateErrorPct returns |actual-target|/target as a percentage (display).
func (m *Manifest) BitrateErrorPct() float64 {
	if m.TargetBps == 0 {
		return 0
	}
	d := m.ActualBps - m.TargetBps
	if d < 0 {
		d = -d
	}
	return float64(d) * 100.0 / float64(m.TargetBps)
}

// AVSkewMs renders the skew in milliseconds (display only).
func (m *Manifest) AVSkewMs() float64 {
	if m.AVSkewDen == 0 {
		return 0
	}
	return float64(m.AVSkewNum) * 1000.0 / float64(m.AVSkewDen)
}

// AlignmentTicks is the worst boundary error between segment coverage and
// committed media ranges, in manifest ticks.
func (m *Manifest) AlignmentTicks() int64 {
	var worst int64
	for _, r := range m.CommittedRanges {
		end := coverageEnd(m.Segments, r)
		d := end - r.End
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
	}
	return worst
}

func coverageEnd(segs []Segment, r Range) int64 {
	end := r.Start
	for _, s := range segs {
		if s.Start <= r.Start && s.End > end {
			end = s.End
		}
	}
	return end
}

// Seal computes ContentSHA256 over the manifest with the field cleared.
func (m *Manifest) Seal() {
	m.ContentSHA256 = ""
	raw, _ := json.Marshal(m)
	sum := sha256.Sum256(raw)
	m.ContentSHA256 = hex.EncodeToString(sum[:])
}

// Validate checks the cross-stage invariants: committed ranges do not
// overlap, segment coverage aligns with media boundaries within one tick,
// and the content hash matches. Any failure names the invariant.
func (m *Manifest) Validate() error {
	sum := m.ContentSHA256
	m.Seal()
	if sum != "" && sum != m.ContentSHA256 {
		return fmt.Errorf("manifest: content hash mismatch (recorded %s)", sum)
	}
	rs := append([]Range(nil), m.CommittedRanges...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].Start < rs[j].Start })
	for i := 1; i < len(rs); i++ {
		if rs[i].Start < rs[i-1].End {
			return fmt.Errorf("manifest: committed ranges [%d,%d) and [%d,%d) overlap",
				rs[i-1].Start, rs[i-1].End, rs[i].Start, rs[i].End)
		}
	}
	if d := m.AlignmentTicks(); d > 1 {
		return fmt.Errorf("manifest: segment coverage misaligned by %d ticks (>1)", d)
	}
	return nil
}

// Write seals and writes the manifest as indented JSON.
func (m *Manifest) Write(path string) error {
	m.Seal()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Read loads a manifest and verifies its content hash.
func Read(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest: parse: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
