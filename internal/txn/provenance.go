package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Decision records one recovery or correction decision made by the pipeline.
type Decision struct {
	Stage    string `json:"stage"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Provenance ties every output to the inputs and decisions that produced it.
type Provenance struct {
	Params       map[string]string `json:"params"`
	Inputs       []InputDigest     `json:"inputs"`
	Decisions    []Decision        `json:"decisions"`
	InputChanges []string          `json:"input_changes,omitempty"`
}

// NewProvenance starts a provenance record for one run.
func NewProvenance(params map[string]string) *Provenance {
	return &Provenance{Params: params}
}

// AddInput registers an input digest.
func (p *Provenance) AddInput(name, sha256 string) {
	p.Inputs = append(p.Inputs, InputDigest{Name: name, SHA256: sha256})
}

// AddDecision records a pipeline decision.
func (p *Provenance) AddDecision(stage, decision, reason string) {
	p.Decisions = append(p.Decisions, Decision{Stage: stage, Decision: decision, Reason: reason})
}

// Digest returns the canonical SHA-256 of the provenance record. Map keys
// are sorted by encoding/json; slices keep insertion order, so identical
// runs with identical inputs and decisions yield identical digests.
func (p *Provenance) Digest() string {
	canonical := *p
	if canonical.InputChanges == nil {
		canonical.InputChanges = []string{}
	}
	sorted := append([]InputDigest(nil), canonical.Inputs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	canonical.Inputs = sorted
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Marshal renders the provenance record as JSON.
func (p *Provenance) Marshal() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

func jsonUnmarshal(raw json.RawMessage, v any) error { return json.Unmarshal(raw, v) }
