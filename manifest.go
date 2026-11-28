package mediakernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
)

// Manifest is the final acceptance record: input digest, parameters, errors,
// segment ranges, silence intervals, fingerprints and output checksum.
type Manifest struct {
	InputSHA256  string             `json:"inputSha256"`
	Params       map[string]int64   `json:"params"`
	Errors       []*Error           `json:"errors"`
	Segments     []CommittedSegment `json:"segments"`
	Silence      []SilenceInterval  `json:"silence"`
	Fingerprints []Fingerprint      `json:"fingerprints"`
	Duplicates   int                `json:"duplicates"`
	OutputSHA256 string             `json:"outputSha256"`
}

// ComputeOutputSHA hashes the canonical result content (segments, silence,
// fingerprints) so reruns can be compared byte-exactly.
func (m *Manifest) ComputeOutputSHA() string {
	payload, _ := json.Marshal(struct {
		Segments     []CommittedSegment `json:"segments"`
		Silence      []SilenceInterval  `json:"silence"`
		Fingerprints []Fingerprint      `json:"fingerprints"`
		Duplicates   int                `json:"duplicates"`
	}{m.Segments, m.Silence, m.Fingerprints, m.Duplicates})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Save writes the manifest JSON.
func (m *Manifest) Save(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
