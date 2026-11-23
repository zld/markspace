package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/zld/media-check/media"
)

// resumeLedger is the on-disk record of which artifacts survived an interrupted
// run. It lets a second invocation skip verified segments even when the first
// run was killed before writing its manifest.
type resumeLedger struct {
	SourceSHA string        `json:"source_sha256"`
	Entries   []LedgerEntry `json:"entries"`
	WrittenAt string        `json:"written_at"`
}

// LedgerEntry records the verified state of one segment.
type LedgerEntry struct {
	Index       int    `json:"index"`
	SHA256      string `json:"sha256"`
	ArtifactSHA string `json:"artifact_sha256"`
	Artifact    string `json:"artifact"`
}

const ledgerName = "resume-ledger.json"

// WriteLedger persists the durable per-segment state after a run.
func WriteLedger(root string, sourceSHA string, man *Manifest) error {
	path := filepath.Join(root, ledgerName)
	if _, err := media.ValidateOutPath(root, path); err != nil {
		return err
	}
	led := resumeLedger{SourceSHA: sourceSHA, WrittenAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, e := range man.Entries {
		if e.Status != StatusOK || e.SHA256 == "" {
			continue
		}
		led.Entries = append(led.Entries, LedgerEntry{
			Index: e.Index, SHA256: e.SHA256, ArtifactSHA: e.ArtifactSHA, Artifact: e.Artifact,
		})
	}
	return writeJSONAtomic(root, path, &led)
}

// ReadLedger loads the durable segment state, returning false when no ledger
// exists. A ledger written for a different source is not trusted.
func ReadLedger(root, sourceSHA string) ([]LedgerEntry, bool) {
	raw, err := os.ReadFile(filepath.Join(root, ledgerName))
	if err != nil {
		return nil, false
	}
	var led resumeLedger
	if err := json.Unmarshal(raw, &led); err != nil {
		return nil, false
	}
	if led.SourceSHA != sourceSHA {
		return nil, false
	}
	return led.Entries, true
}

// ValidateLedger verifies that every ledger entry still points at a readable,
// hash-matching artifact. It returns the set of valid segment indices.
func ValidateLedger(root string, entries []LedgerEntry) map[int]bool {
	valid := make(map[int]bool, len(entries))
	for _, e := range entries {
		path := filepath.Join(root, filepath.FromSlash(e.Artifact))
		if _, err := media.ValidateOutPath(root, path); err != nil {
			continue
		}
		rec, ok := loadArtifact(path)
		if !ok || rec.SHA256 != e.SHA256 || rec.ArtifactSHA != artifactDigest(rec) {
			continue
		}
		valid[e.Index] = true
	}
	return valid
}
