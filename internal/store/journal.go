// Journal is the append-only checkpoint log. Each line is one JSON record; the
// file is only ever appended to, and recovery replays committed blocks from the
// last valid line. A torn final line is discarded, never trusted.
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/model"
)

// JournalFileName is the append-only log written inside the store root.
const JournalFileName = "journal.jsonl"

// Record is one journal line. Kind is "asset", "checkpoint" or "manifest".
type Record struct {
	Kind      string `json:"kind"`
	AssetKey  string `json:"asset_key"`
	Stage     string `json:"stage"`
	Digest    string `json:"digest,omitempty"`
	Policy    string `json:"policy,omitempty"`
	Container string `json:"container,omitempty"`
	// Checkpoints carry the committed block frontier.
	Block       int    `json:"block,omitempty"`
	Frames      int64  `json:"frames,omitempty"`
	Length      int64  `json:"length,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	// Manifest records the sealed manifest digest so repeat runs are no-ops.
	ManifestDigest string `json:"manifest_digest,omitempty"`
	Entries        int    `json:"entries,omitempty"`
	// Blob carries a JSON-encoded projection (used for archive records).
	Blob json.RawMessage `json:"blob,omitempty"`
}

// Journal is an append-only record log.
type Journal struct {
	path string
}

// OpenJournal prepares the log inside the store root.
func OpenJournal(root string) (*Journal, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "create journal dir")
	}
	return &Journal{path: filepath.Join(root, JournalFileName)}, nil
}

// Path returns the log path.
func (j *Journal) Path() string { return j.path }

// Append writes one record durably.
func (j *Journal) Append(rec Record) error {
	var b []byte
	b, err := json.Marshal(rec)
	if err != nil {
		return fault.Wrap(fault.CodeCheckpoint, fault.StageNormalized, -1, err, "encode journal record")
	}
	b = append(b, '\n')
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "open journal")
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "append journal")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "sync journal")
	}
	return f.Close()
}

// Replay reads every intact record. A torn trailing line is ignored so that a
// process killed mid-append resumes from the last committed checkpoint.
func (j *Journal) Replay() ([]Record, error) {
	f, err := os.Open(j.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "open journal")
	}
	defer f.Close()
	var (
		recs []Record
		off  int64
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		off += int64(len(line)) + 1
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			// Only the final physical line may be truncated.
			if isLastLine(f, off) {
				break
			}
			return nil, fault.Wrap(fault.CodeCheckpoint, fault.StageNormalized, off, err, "journal line is corrupt")
		}
		recs = append(recs, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageNormalized, off, err, "scan journal")
	}
	return recs, nil
}

func isLastLine(f *os.File, off int64) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return off >= st.Size()
}

// Recorder converts journal records back into model state.
type Recorder struct {
	recs []Record
}

// NewRecorder indexes a replay.
func NewRecorder(recs []Record) *Recorder { return &Recorder{recs: recs} }

// Records returns the underlying log.
func (r *Recorder) Records() []Record { return r.recs }

// CommittedBlocks returns the furthest contiguous block frontier per asset.
func (r *Recorder) CommittedBlocks() map[string]int {
	out := make(map[string]int)
	for _, rec := range r.recs {
		if rec.Kind != "checkpoint" {
			continue
		}
		if rec.Block+1 > out[rec.AssetKey] {
			out[rec.AssetKey] = rec.Block + 1
		}
	}
	return out
}

// LastCheckpoint returns the highest committed checkpoint record for an asset.
func (r *Recorder) LastCheckpoint(key string) (Record, bool) {
	var best Record
	found := false
	for _, rec := range r.recs {
		if rec.Kind != "checkpoint" || rec.AssetKey != key {
			continue
		}
		if !found || rec.Block > best.Block {
			best, found = rec, true
		}
	}
	return best, found
}

// StageOf returns the last stage recorded for an asset.
func (r *Recorder) StageOf(key string) model.Stage {
	best := model.Stage("")
	bestOrd := -1
	for _, rec := range r.recs {
		if rec.AssetKey != key {
			continue
		}
		s := model.Stage(rec.Stage)
		if s.Order() > bestOrd {
			best, bestOrd = s, s.Order()
		}
	}
	return best
}

// HasManifest reports whether the manifest digest was already sealed.
func (r *Recorder) HasManifest(digest string) bool {
	for _, rec := range r.recs {
		if rec.Kind == "manifest" && rec.ManifestDigest == digest {
			return true
		}
	}
	return false
}

// Prune drops the journal — used only by an explicit reset.
func (j *Journal) Prune() error {
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "prune journal")
	}
	return nil
}
