// Package txn implements an append-only write-ahead journal that stages merge
// results, verifies per-segment hashes before commit, swaps the manifest in
// atomically and supports crash recovery by replay without duplicate cues.
package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediamerge/internal/fault"
)

// Segment is one staged, content-hashed output fragment.
type Segment struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is the committed, auditable output manifest.
type Manifest struct {
	Version  int       `json:"version"`
	Segments []Segment `json:"segments"`
	CueCount int       `json:"cue_count"`
	Digest   string    `json:"digest"`
}

// Journal coordinates one run directory on disk.
type Journal struct {
	dir          string
	journalPath  string
	manifestPath string
}

type record struct {
	Step     string   `json:"step"`
	Segment  *Segment `json:"segment,omitempty"`
	CueCount int      `json:"cue_count,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Seq      int      `json:"seq"`
}

// Open prepares a run directory under root and reads any prior journal.
func Open(root string) (*Journal, error) {
	dir := filepath.Join(root, "txn")
	if err := os.MkdirAll(filepath.Join(dir, "staging"), 0o755); err != nil {
		return nil, fault.New(fault.StageTxn, fault.KindIO, dir, err.Error())
	}
	j := &Journal{
		dir:          dir,
		journalPath:  filepath.Join(dir, "journal.log"),
		manifestPath: filepath.Join(dir, "manifest.json"),
	}
	return j, nil
}

func (j *Journal) append(rec record) error {
	rec.Seq = j.nextSeq()
	b, err := json.Marshal(rec)
	if err != nil {
		return fault.New(fault.StageTxn, fault.KindState, j.journalPath, err.Error())
	}
	f, err := os.OpenFile(j.journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fault.New(fault.StageTxn, fault.KindIO, j.journalPath, err.Error())
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fault.New(fault.StageTxn, fault.KindIO, j.journalPath, err.Error())
	}
	return f.Sync()
}

func (j *Journal) nextSeq() int {
	recs, _ := j.ReadRecords()
	if len(recs) == 0 {
		return 1
	}
	return recs[len(recs)-1].Seq + 1
}

// ReadRecords returns the durable journal contents in append order.
func (j *Journal) ReadRecords() ([]record, error) {
	b, err := os.ReadFile(j.journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.New(fault.StageTxn, fault.KindIO, j.journalPath, err.Error())
	}
	var out []record
	for _, line := range splitLines(b) {
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fault.New(fault.StageTxn, fault.KindChecksum, j.journalPath,
				"unparseable journal record")
		}
		out = append(out, rec)
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// StagePath is where a segment payload must be written before StageSegment.
func (j *Journal) StagePath(id int) string {
	return filepath.Join(j.dir, "staging", segmentName(id))
}

func segmentName(id int) string {
	return "seg-" + itoa(id) + ".bin"
}

// StageSegment hashes a staged payload, then journals its acceptance.
func (j *Journal) StageSegment(id int) (*Segment, error) {
	path := j.StagePath(id)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New(fault.StageTxn, fault.KindIO, path, err.Error())
	}
	sum := sha256.Sum256(b)
	seg := &Segment{ID: id, Path: filepath.Join("segments", segmentName(id)),
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
	// Replaying a completed stage must be idempotent.
	if j.hasSegment(id) {
		return seg, nil
	}
	if err := j.append(record{Step: "stage", Segment: seg}); err != nil {
		return nil, err
	}
	return seg, nil
}

func (j *Journal) hasSegment(id int) bool {
	recs, _ := j.ReadRecords()
	for _, r := range recs {
		if r.Step == "stage" && r.Segment != nil && r.Segment.ID == id {
			return true
		}
	}
	return false
}
