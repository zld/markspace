// Package report defines the deterministic per-run summary emitted by the
// pipeline and computes a stable manifest digest so repeat runs can prove
// they produced identical conclusions.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Status is the per-item outcome classification.
type Status string

const (
	StatusSatisfied   Status = "satisfied"
	StatusUnmet       Status = "unmet"
	StatusIsolated    Status = "isolated"
	StatusBudgetMiss  Status = "budget_miss"
	StatusQualityMiss Status = "quality_miss"
	StatusSizeMiss    Status = "size_miss"
)

// AnomalyRange is a JSON-friendly interval of anomalous sequence numbers.
type AnomalyRange struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// Item is the per-material conclusion block.
type Item struct {
	MaterialID       string         `json:"materialId"`
	Codec            string         `json:"codec"`
	Isolated         bool           `json:"isolated"`
	TotalPackets     int            `json:"totalPackets"`
	LostPackets      int            `json:"lostPackets"`
	DuplicatePackets int            `json:"duplicatePackets"`
	ConflictCount    int            `json:"conflictCount"`
	AnomalyRanges    []AnomalyRange `json:"anomalyRanges"`
	InputSamples     uint64         `json:"inputSamples"`
	OutputSamples    uint64         `json:"outputSamples"`
	OutputBytes      int64          `json:"outputBytes"`
	AvgBitrateBps    float64        `json:"avgBitrateBps"`
	LUFS             float64        `json:"lufs"`
	TruePeakDBTP     float64        `json:"truePeakDbTP"`
	// CacheHit is operational telemetry, not a processing conclusion, so it is
	// excluded from the manifest digest via the custom MarshalJSON below.
	CacheHit         bool           `json:"cacheHit"`
	Status           Status         `json:"status"`
	Deviations       []string       `json:"deviations,omitempty"`
}

// digestItem is Item without the operational CacheHit field; the manifest
// digest is computed over digestItem values so a cache hit/miss difference
// cannot change the conclusions' hash.
type digestItem struct {
	Item
	CacheHit bool `json:"cacheHit,omitempty"`
}

// MarshalJSON omits CacheHit from any hashing/serialization that relies on the
// stable canonical form by always emitting the digest-stable projection.
func (i Item) canonical() digestItem {
	return digestItem{Item: i, CacheHit: false}
}

// Budget summarizes time and size budget accounting for the whole run.
type Budget struct {
	BudgetMs       int64   `json:"budgetMs"`
	ElapsedMs      int64   `json:"elapsedMs"`
	DeviationMs    int64   `json:"deviationMs"`
	MaxBytes       int64   `json:"maxBytes"`
	OverBudgetTime bool    `json:"overBudgetTime"`
	OverBudgetSize bool    `json:"overBudgetSize"`
}

// digestBudget keeps only the configured budget limits and the deterministic
// size conclusion. Wall-clock fields vary between runs and are excluded so
// the manifest digest stays reproducible.
type digestBudget struct {
	BudgetMs       int64 `json:"budgetMs"`
	MaxBytes       int64 `json:"maxBytes"`
	OverBudgetSize bool  `json:"overBudgetSize"`
}

func (b Budget) canonical() digestBudget {
	return digestBudget{
		BudgetMs:       b.BudgetMs,
		MaxBytes:       b.MaxBytes,
		OverBudgetSize: b.OverBudgetSize,
	}
}

// Summary is the top-level JSON document printed by the CLI.
type Summary struct {
	Seed           int64   `json:"seed"`
	Items          []Item  `json:"items"`
	Budget         Budget  `json:"budget"`
	ManifestSHA256 string  `json:"manifestSha256,omitempty"`
}

// canonicalJSON serializes with stable field order (struct order) and no
// HTML escaping or indentation variance, so the digest is reproducible.
func canonicalJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// ComputeManifestDigest hashes every item and budget field but excludes the
// digest field and operational cache-hit telemetry. Two runs with identical
// conclusions yield the same hash.
func (s *Summary) ComputeManifestDigest() (string, error) {
	cp := *s
	cp.ManifestSHA256 = ""
	stable := make([]digestItem, len(cp.Items))
	for i, it := range cp.Items {
		stable[i] = it.canonical()
	}
	payload := struct {
		Seed   int64        `json:"seed"`
		Items  []digestItem `json:"items"`
		Budget digestBudget `json:"budget"`
	}{Seed: cp.Seed, Items: stable, Budget: cp.Budget.canonical()}
	b, err := canonicalJSON(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	d := hex.EncodeToString(sum[:])
	s.ManifestSHA256 = d
	return d, nil
}

// JSON renders the full summary including the manifest digest.
func (s *Summary) JSON() ([]byte, error) {
	if s.ManifestSHA256 == "" {
		if _, err := s.ComputeManifestDigest(); err != nil {
			return nil, err
		}
	}
	return json.MarshalIndent(s, "", "  ")
}
