package index

import (
	"encoding/json"
	"os"
	"path/filepath"

	"assetcheck/internal/atomicfile"
	"assetcheck/internal/errmodel"
	"assetcheck/internal/txn"
)

// ManifestFile is the atomically published visibility map.
const ManifestFile = "manifest.json"

// Manifest schema identifier.
const ManifestSchema = "assetcheck-manifest/v1"

// Entry is one visible committed index.
type Entry struct {
	ContentKey         string `json:"content-key"`
	ContentFingerprint string `json:"content-fingerprint"`
	Seq                uint64 `json:"seq"`
	File               string `json:"file"` // path relative to store dir
	SHA256             string `json:"sha256"`
	Digest             string `json:"digest"`
}

// Manifest maps content keys to their single visible committed entry.
type Manifest struct {
	Schema  string           `json:"schema"`
	LastSeq uint64           `json:"last-seq"`
	Entries map[string]Entry `json:"entries"`
}

// Store is the index directory: idx/ finalized files, staging/ temp files
// and the atomically replaced manifest.json.
type Store struct {
	dir      string
	manifest Manifest
}

// ContentKey shortens a full fingerprint to a filesystem-safe map key.
func ContentKey(contentFP string) string {
	// fingerprints look like "sha256:<hex>"; keep 20 hex chars.
	h := contentFP
	if len(h) > 7 && h[:7] == "sha256:" {
		h = h[7:]
	}
	if len(h) > 20 {
		h = h[:20]
	}
	return h
}

// OpenStore loads or initializes an index directory.
func OpenStore(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "idx"), filepath.Join(dir, "staging")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, errmodel.New(errmodel.ErrCommit, "mkdir %s: %v", d, err)
		}
	}
	s := &Store{dir: dir, manifest: Manifest{Schema: ManifestSchema, Entries: map[string]Entry{}}}
	p := filepath.Join(dir, ManifestFile)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, errmodel.New(errmodel.ErrIndexChecksum, "read manifest: %v", err)
	}
	if err := json.Unmarshal(b, &s.manifest); err != nil {
		return nil, errmodel.New(errmodel.ErrIndexSchema, "bad manifest: %v", err)
	}
	if s.manifest.Entries == nil {
		s.manifest.Entries = map[string]Entry{}
	}
	return s, nil
}

// Dir returns the store root.
func (s *Store) Dir() string { return s.dir }

// ManifestSnapshot returns a copy of the manifest.
func (s *Store) ManifestSnapshot() Manifest {
	cp := Manifest{Schema: s.manifest.Schema, LastSeq: s.manifest.LastSeq, Entries: map[string]Entry{}}
	for k, v := range s.manifest.Entries {
		cp.Entries[k] = v
	}
	return cp
}

// NewArtifact computes staging/final paths for a transaction.
func (s *Store) NewArtifact(txnID, contentFP string, seq uint64) txn.Artifact {
	key := ContentKey(contentFP)
	return txn.Artifact{
		TxnID:       txnID,
		Key:         key,
		Seq:         seq,
		StagingPath: filepath.Join(s.dir, "staging", txnID+".json"),
		FinalPath:   filepath.Join(s.dir, "idx", key+"-"+itoa(seq)+".json"),
	}
}

// WriteStaging serializes idx to the uncommitted staging file.
func (s *Store) WriteStaging(a *txn.Artifact, idx *Index) error {
	b, err := idx.Marshal()
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(a.StagingPath, b, 0o644); err != nil {
		return errmodel.New(errmodel.ErrCommit, "write staging: %v", err)
	}
	a.SHA256 = atomicfile.SHA256Bytes(b)
	return nil
}

// Publish verifies and atomically exposes a prepared artifact. It is
// idempotent so WAL recovery can call it again.
func (s *Store) Publish(a txn.Artifact) error {
	src := a.StagingPath
	if _, err := os.Stat(src); err != nil {
		// Recovery retry after a previous rename: final may already exist.
		if _, err2 := os.Stat(a.FinalPath); err2 != nil {
			return errmodel.New(errmodel.ErrIndexChecksum, "neither staging nor final exists for seq %d", a.Seq)
		}
		src = a.FinalPath
	} else {
		if err := os.Rename(a.StagingPath, a.FinalPath); err != nil {
			return errmodel.New(errmodel.ErrCommit, "rename index: %v", err)
		}
	}
	sha, err := atomicfile.SHA256File(a.FinalPath)
	if err != nil {
		return errmodel.New(errmodel.ErrIndexChecksum, "checksum final: %v", err)
	}
	if a.SHA256 != "" && sha != a.SHA256 {
		return errmodel.New(errmodel.ErrIndexChecksum,
			"published index checksum mismatch: wal=%s file=%s", a.SHA256, sha)
	}
	b, err := os.ReadFile(a.FinalPath)
	if err != nil {
		return errmodel.New(errmodel.ErrIndexSchema, "read published: %v", err)
	}
	idx, err := UnmarshalIndex(b)
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(s.dir, a.FinalPath)
	s.manifest.Entries[a.Key] = Entry{
		ContentKey: a.Key, ContentFingerprint: idx.ContentFingerprint,
		Seq: a.Seq, File: rel, SHA256: sha, Digest: idx.Digest,
	}
	if a.Seq > s.manifest.LastSeq {
		s.manifest.LastSeq = a.Seq
	}
	return s.writeManifest()
}

// DiscardStaging removes a rolled-back staging file.
func (s *Store) DiscardStaging(a txn.Artifact) error {
	if err := os.Remove(a.StagingPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) writeManifest() error {
	b, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(s.dir, ManifestFile), b, 0o644)
}

// Visible loads the currently committed index for a content fingerprint.
func (s *Store) Visible(contentFP string) (*Index, Entry, error) {
	key := ContentKey(contentFP)
	e, ok := s.manifest.Entries[key]
	if !ok {
		return nil, Entry{}, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(s.dir, e.File))
	if err != nil {
		return nil, e, err
	}
	idx, err := UnmarshalIndex(b)
	if err != nil {
		return nil, e, err
	}
	return idx, e, nil
}

func itoa(u uint64) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}
