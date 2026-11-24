// Package report renders the deterministic pipeline summary. Field order
// and formatting are part of the contract: identical inputs must produce
// identical bytes, which the commit layer hashes for restart verification.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/local/medialab/internal/timeline"
)

// DamageRange is one JSON-friendly time window in milliseconds.
type DamageRange struct {
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
}

// Summary is the complete, stable result of one pipeline run.
type Summary struct {
	CommitID        string        `json:"commit_id"`
	AVSyncMs        int64         `json:"av_sync_ms"`
	RecoveryRate    float64       `json:"recovery_rate"`
	P95LatencyMs    int64         `json:"p95_latency_ms"`
	KeyframeGapMs   int64         `json:"keyframe_gap_ms"`
	DamageRanges    []DamageRange `json:"damage_ranges"`
	ConflictCount   int           `json:"conflict_count"`
	ErrorCount      int           `json:"error_count"`
	RecoveredPackets int          `json:"recovered_packets"`
	Unrecovered     int           `json:"unrecovered_packets"`
	Healthy         bool          `json:"healthy"`
}

// Builder accumulates the values each subsystem contributes.
type Builder struct {
	s Summary
}

// NewBuilder starts an empty summary with non-nil slices for stable JSON.
func NewBuilder() *Builder {
	return &Builder{s: Summary{DamageRanges: []DamageRange{}, RecoveryRate: 1}}
}

// SetCommitID attaches the durable transaction boundary id.
func (b *Builder) SetCommitID(id string) *Builder { b.s.CommitID = id; return b }

// SetAVSync records the audio/video alignment in milliseconds.
func (b *Builder) SetAVSync(ms int64) *Builder { b.s.AVSyncMs = ms; return b }

// SetRecovery records the recovery fraction and packet counters.
func (b *Builder) SetRecovery(rate float64, recovered, unrecovered int) *Builder {
	b.s.RecoveryRate = rate
	b.s.RecoveredPackets = recovered
	b.s.Unrecovered = unrecovered
	return b
}

// SetLatency records the P95 buffering latency in milliseconds.
func (b *Builder) SetLatency(p95Ms int64) *Builder { b.s.P95LatencyMs = p95Ms; return b }

// SetKeyframeGap records the largest valid keyframe interval.
func (b *Builder) SetKeyframeGap(gapMs int64) *Builder { b.s.KeyframeGapMs = gapMs; return b }

// SetConflicts records the number of arbitrated metadata conflicts.
func (b *Builder) SetConflicts(n int) *Builder { b.s.ConflictCount = n; return b }

// SetErrorCount records the total number of located error records.
func (b *Builder) SetErrorCount(n int) *Builder { b.s.ErrorCount = n; return b }

// SetDamage converts master-clock windows into millisecond ranges.
func (b *Builder) SetDamage(windows []timeline.Window) *Builder {
	b.s.DamageRanges = make([]DamageRange, 0, len(windows))
	for _, w := range windows {
		b.s.DamageRanges = append(b.s.DamageRanges, DamageRange{
			StartMs: timeline.Millis(w.Start), EndMs: timeline.Millis(w.End),
		})
	}
	return b
}

// MarkHealthy flips the healthy flag once all assertions pass.
func (b *Builder) MarkHealthy(ok bool) *Builder { b.s.Healthy = ok; return b }

// Build returns the finalized summary.
func (b *Builder) Build() Summary {
	sort.Slice(b.s.DamageRanges, func(i, j int) bool {
		if b.s.DamageRanges[i].StartMs != b.s.DamageRanges[j].StartMs {
			return b.s.DamageRanges[i].StartMs < b.s.DamageRanges[j].StartMs
		}
		return b.s.DamageRanges[i].EndMs < b.s.DamageRanges[j].EndMs
	})
	return b.s
}

// StableJSON serializes the summary with sorted keys and no interface
// map randomness, so hashing is reproducible across processes.
func StableJSON(s Summary) ([]byte, error) {
	// Round-trip through a typed struct (already ordered fields) using
	// indentation only for readability; encoding/json honors struct order.
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Hash returns the hex SHA-256 of the stable JSON representation. The
// commit id is excluded so the content hash stays comparable across a
// restart that reuses the same boundary.
func Hash(s Summary) string {
	content := s
	content.CommitID = ""
	raw, err := StableJSON(content)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Text renders the single-line key=value form required by the demo.
func Text(s Summary) string {
	var b strings.Builder
	b.WriteString("av_sync_ms=" + itoa(s.AVSyncMs))
	b.WriteString(" recovery_rate=" + trimFloat(s.RecoveryRate))
	b.WriteString(" p95_latency_ms=" + itoa(s.P95LatencyMs))
	b.WriteString(" keyframe_gap_ms=" + itoa(s.KeyframeGapMs))
	b.WriteString(" damage_ranges=" + formatRanges(s.DamageRanges))
	b.WriteString(" conflict_count=" + itoa(int64(s.ConflictCount)))
	b.WriteString(" commit_id=" + s.CommitID)
	b.WriteString(" healthy=" + boolStr(s.Healthy))
	return b.String()
}

func formatRanges(rs []DamageRange) string {
	if len(rs) == 0 {
		return "[]"
	}
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = "[" + itoa(r.StartMs) + "," + itoa(r.EndMs) + ")"
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// trimFloat renders a rate with exactly four decimal places, avoiding
// strconv's shortest-repr variance between Go versions.
func trimFloat(f float64) string {
	scaled := int64(f*10000 + 0.5)
	whole := scaled / 10000
	frac := scaled % 10000
	digits := []byte{'0', '0', '0', '0'}
	for i := 3; i >= 0; i-- {
		digits[i] = byte('0' + frac%10)
		frac /= 10
	}
	return itoa(whole) + "." + string(digits)
}
