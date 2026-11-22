package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"mediapack/safefs"
)

// StageRecord ties one stage's output digest into the provenance chain.
type StageRecord struct {
	Name         string `json:"name"`
	ResultDigest string `json:"result_digest"`
	Reused       bool   `json:"reused"` // true when restored from a valid checkpoint
}

// Provenance records input digests, parameters and the artifact relation
// chain of one run, so differences between runs are traceable.
type Provenance struct {
	Version      int           `json:"version"`
	InputDigest  string        `json:"input_digest"`
	ParamsDigest string        `json:"params_digest"`
	Stages       []StageRecord `json:"stages"`
	ChainDigest  string        `json:"chain_digest"`
}

// ChainDigest computes the rolling provenance chain over inputs,
// parameters and every stage output, in stage order.
func ChainDigest(inputDigest, paramsDigest string, stages []StageRecord) string {
	h := sha256.New()
	h.Write([]byte(inputDigest))
	h.Write([]byte{0})
	h.Write([]byte(paramsDigest))
	for _, s := range stages {
		h.Write([]byte{0})
		h.Write([]byte(s.Name))
		h.Write([]byte{0})
		h.Write([]byte(s.ResultDigest))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewProvenance builds a provenance record with its chain digest.
func NewProvenance(inputDigest, paramsDigest string, stages []StageRecord) *Provenance {
	return &Provenance{
		Version:      1,
		InputDigest:  inputDigest,
		ParamsDigest: paramsDigest,
		Stages:       stages,
		ChainDigest:  ChainDigest(inputDigest, paramsDigest, stages),
	}
}

// Verify recomputes the chain and checks it against the record and the
// expected input/parameter digests.
func (p *Provenance) Verify(inputDigest, paramsDigest string) bool {
	if p.InputDigest != inputDigest || p.ParamsDigest != paramsDigest {
		return false
	}
	return p.ChainDigest == ChainDigest(inputDigest, paramsDigest, p.Stages)
}

// WriteProvenance stores the record inside the output directory.
func WriteProvenance(p *Provenance, outDir, name string) (string, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return safefs.WriteFileAtomic(outDir, name, b, 0o644)
}

// LoadProvenance reads a stored record.
func LoadProvenance(path string) (*Provenance, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Provenance
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
