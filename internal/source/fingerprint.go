package source

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
)

// Fingerprint keeps three independent change domains. A metadata-only revision
// can therefore invalidate metadata caches without claiming byte replacement.
type Fingerprint struct {
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	ContentSHA256   string `json:"content_sha256"`
	StructureSHA256 string `json:"structure_sha256"`
	MetadataVersion string `json:"metadata_version"`
}

type ChangeSet struct {
	SamePath         bool `json:"same_path"`
	ContentReplaced  bool `json:"content_replaced"`
	StructureChanged bool `json:"structure_changed"`
	MetadataChanged  bool `json:"metadata_changed"`
}

func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func FromBytes(path string, data []byte, structureSHA, metadataVersion string) Fingerprint {
	return Fingerprint{
		Path:            path,
		Size:            int64(len(data)),
		ContentSHA256:   SHA256(data),
		StructureSHA256: structureSHA,
		MetadataVersion: metadataVersion,
	}
}

func FromFile(path, structureSHA, metadataVersion string) (Fingerprint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Fingerprint{}, err
	}
	return FromBytes(path, data, structureSHA, metadataVersion), nil
}

func Diff(oldFP, newFP Fingerprint) ChangeSet {
	return ChangeSet{
		SamePath:         oldFP.Path == newFP.Path,
		ContentReplaced:  oldFP.ContentSHA256 != newFP.ContentSHA256,
		StructureChanged: oldFP.StructureSHA256 != newFP.StructureSHA256,
		MetadataChanged:  oldFP.MetadataVersion != newFP.MetadataVersion,
	}
}

// Tracker remembers the last fingerprint observed at each path.
type Tracker struct {
	mu  sync.Mutex
	old map[string]Fingerprint
}

func NewTracker() *Tracker {
	return &Tracker{old: make(map[string]Fingerprint)}
}

// Observe returns the change relative to the prior observation and stores fp.
func (t *Tracker) Observe(fp Fingerprint) (ChangeSet, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	oldFP, existed := t.old[fp.Path]
	change := Diff(oldFP, fp)
	change.SamePath = existed && oldFP.Path == fp.Path
	t.old[fp.Path] = fp
	return change, existed
}

func (t *Tracker) Get(path string) (Fingerprint, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fp, ok := t.old[path]
	return fp, ok
}
