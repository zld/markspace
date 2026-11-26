package job

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// SourceDigest hashes the bytes that uniquely identify ingested content:
// the ordered track keys and every sample's payload hash. Re-submitting the
// same content therefore yields the same digest regardless of file paths.
func SourceDigest(trackPayloads map[string][][]byte) string {
	h := sha256.New()
	keys := make([]string, 0, len(trackPayloads))
	for k := range trackPayloads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, track := range keys {
		fmt.Fprintf(h, "track=%s\n", track)
		for i, payload := range trackPayloads[track] {
			sum := sha256.Sum256(payload)
			fmt.Fprintf(h, "sample=%d hash=%s size=%d\n", i, hex.EncodeToString(sum[:]), len(payload))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Policy captures every knob that can change the derived configuration.
// JSON keys are fixed and the struct is serialized deterministically.
type Policy struct {
	MaxBitrate  int64             `json:"max_bitrate"`
	BudgetUnits int64             `json:"budget_units"`
	MinQuality  [2]int64          `json:"min_quality"`
	Candidates  []PolicyCandidate `json:"candidates"`
}

// PolicyCandidate is the serializable subset of plan.Candidate.
type PolicyCandidate struct {
	Width     int   `json:"width"`
	Height    int   `json:"height"`
	Bitrate   int64 `json:"bitrate"`
	QualityN  int64 `json:"quality_num"`
	QualityD  int64 `json:"quality_den"`
	CostPerNs int64 `json:"cost_per_ns"`
}

// PolicyDigest hashes the canonical JSON encoding of the policy. Any change
// to a knob changes the digest and therefore produces a new task key.
func PolicyDigest(p Policy) (string, error) {
	// Defensive copy + sort candidates by resolution for stable encoding.
	cp := p
	cp.Candidates = append([]PolicyCandidate(nil), p.Candidates...)
	sort.Slice(cp.Candidates, func(i, j int) bool {
		if cp.Candidates[i].Height != cp.Candidates[j].Height {
			return cp.Candidates[i].Height < cp.Candidates[j].Height
		}
		return cp.Candidates[i].Width < cp.Candidates[j].Width
	})
	raw, err := json.Marshal(cp)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// TaskKey combines the two digests. Source and policy are hashed separately
// first so either can be audited independently of the composite key.
func TaskKey(sourceDigest, policyDigest string) string {
	h := sha256.New()
	h.Write([]byte("source=" + sourceDigest + "\n"))
	h.Write([]byte("policy=" + policyDigest + "\n"))
	return hex.EncodeToString(h.Sum(nil))
}
