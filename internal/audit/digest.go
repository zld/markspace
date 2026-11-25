package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"mediaflow/internal/index"
)

// StableDigest hashes the reproducible conclusion, excluding transient scheduling
// facts such as whether a chunk was written or skipped on this particular invocation.
func StableDigest(s *Summary) (string, error) {
	stable := struct {
		BatchKey string        `json:"batch_key"`
		Items    []*ItemReport `json:"items"`
	}{BatchKey: s.BatchKey, Items: make([]*ItemReport, 0, len(s.Items))}

	for _, it := range s.Items {
		cp := *it
		cp.WrittenChunks = nil
		cp.SkippedChunks = nil
		cp.RolledBackChunks = nil
		if it.Chunks != nil {
			cp.Chunks = append([]index.ChunkRecord(nil), it.Chunks...)
			for i := range cp.Chunks {
				cp.Chunks[i].Attempt = 0
			}
		}
		stable.Items = append(stable.Items, &cp)
	}
	b, err := json.Marshal(stable)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
