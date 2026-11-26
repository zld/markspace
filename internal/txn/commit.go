package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediamerge/internal/fault"
)

// VerifySegments re-hashes every staged segment and compares it against the
// journaled hash. Any mismatch aborts the transaction before the manifest is
// touched.
func (j *Journal) VerifySegments(segs []Segment) error {
	for _, want := range segs {
		path := j.StagePath(want.ID)
		b, err := os.ReadFile(path)
		if err != nil {
			return j.rollback("verify", "segment "+segmentName(want.ID)+" unreadable: "+err.Error())
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want.SHA256 {
			return j.rollback("verify",
				"segment "+segmentName(want.ID)+" hash mismatch: got "+got+" want "+want.SHA256)
		}
	}
	return nil
}

// Commit validates every segment hash, promotes the staging files into the
// segments directory and atomically swaps the manifest. On any failure the
// previous manifest is left byte-for-byte intact and a rollback is journaled.
func (j *Journal) Commit(segs []Segment, cueCount int) (*Manifest, error) {
	if j.IsCommitted() {
		return j.CurrentManifest()
	}
	if err := j.VerifySegments(segs); err != nil {
		return nil, err
	}
	segDir := filepath.Join(j.dir, "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		return nil, j.rollback("promote", err.Error())
	}
	promoted := make([]Segment, 0, len(segs))
	for _, seg := range segs {
		data, err := os.ReadFile(j.StagePath(seg.ID))
		if err != nil {
			return nil, j.rollback("promote", err.Error())
		}
		dest := filepath.Join(segDir, segmentName(seg.ID))
		if err := writeFileAtomic(dest, data); err != nil {
			return nil, j.rollback("promote", err.Error())
		}
		promoted = append(promoted, seg)
	}
	m := &Manifest{
		Version:  j.nextManifestVersion(),
		Segments: promoted,
		CueCount: cueCount,
		Digest:   manifestDigest(promoted, cueCount),
	}
	if err := j.writeManifestAtomic(m); err != nil {
		return nil, j.rollback("manifest", err.Error())
	}
	if err := j.append(record{Step: "commit", CueCount: cueCount}); err != nil {
		return nil, err
	}
	return m, nil
}

// Rollback records an explicit rollback with its reason and returns a fault.
func (j *Journal) rollback(step, reason string) error {
	_ = j.append(record{Step: "rollback", Reason: step + ": " + reason})
	return fault.New(fault.StageTxn, fault.KindChecksum, j.dir,
		"transaction rolled back: "+step+": "+reason)
}

// RollbackReason returns the last recorded rollback reason, if any.
func (j *Journal) RollbackReason() string {
	recs, _ := j.ReadRecords()
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Step == "rollback" {
			return recs[i].Reason
		}
	}
	return ""
}

// IsCommitted reports whether a commit record exists in the journal.
func (j *Journal) IsCommitted() bool {
	recs, _ := j.ReadRecords()
	for _, r := range recs {
		if r.Step == "commit" {
			return true
		}
	}
	return false
}

// CurrentManifest reads the last successfully committed manifest.
func (j *Journal) CurrentManifest() (*Manifest, error) {
	b, err := os.ReadFile(j.manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.New(fault.StageTxn, fault.KindIO, j.manifestPath, err.Error())
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fault.New(fault.StageTxn, fault.KindChecksum, j.manifestPath, err.Error())
	}
	return &m, nil
}

func (j *Journal) nextManifestVersion() int {
	if m, _ := j.CurrentManifest(); m != nil {
		return m.Version + 1
	}
	return 1
}

func (j *Journal) writeManifestAtomic(m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(j.manifestPath, b)
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "txn-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func manifestDigest(segs []Segment, cueCount int) string {
	h := sha256.New()
	for _, s := range segs {
		h.Write([]byte(s.SHA256))
	}
	h.Write([]byte(itoa(cueCount)))
	return hex.EncodeToString(h.Sum(nil))
}
