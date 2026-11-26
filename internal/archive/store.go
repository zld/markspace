// Package archive commits merged samples into a recoverable archive. The
// committed index lives in an MP4 box (mediarc "mrci"); a checkpoint file
// pins its hash so corruption is detected on open. Each version is written
// in a transaction that can be rolled back without touching committed data.
package archive

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediarc/internal/model"

	mp4 "github.com/abema/go-mp4"
)

// mrciBox is the mediarc archive-index box: committed entries as JSON.
type mrciBox struct {
	mp4.Box
	Data []byte `mp4:"0,size=8"`
}

func (*mrciBox) GetType() mp4.BoxType { return mp4.StrToBoxType("mrci") }

func init() { mp4.AddBoxDef(&mrciBox{}) }

// Entry is one committed sample location in the archive.
type Entry struct {
	Version string `json:"version"`
	Track   int    `json:"track"`
	Ticks   int64  `json:"ticks"`
	Scale   int64  `json:"scale"`
	OutOff  int64  `json:"out_offset"`
	Length  int64  `json:"length"`
	Hash    string `json:"hash"`
}

type checkpoint struct {
	Count int    `json:"count"`
	SHA1  string `json:"sha1"`
}

// Store is an opened archive directory.
type Store struct {
	dir       string
	entries   []Entry
	rollbacks int
}

// SafeJoin joins name onto base, rejecting paths that escape base.
func SafeJoin(base, name string) (string, *model.Error) {
	if filepath.IsAbs(name) {
		return "", &model.Error{Code: model.ErrPath, Msg: "absolute path rejected: " + name}
	}
	p := filepath.Join(base, name)
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &model.Error{Code: model.ErrPath, Msg: "path escapes base: " + name}
	}
	return p, nil
}

func digest(entries []Entry) string {
	h := sha1.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s|%d|%d|%d|%d|%d|%s\n", e.Version, e.Track, e.Ticks, e.Scale, e.OutOff, e.Length, e.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Open opens or creates the archive in dir. Existing committed state is
// verified against its checkpoint; a mismatch is unrecoverable.
func Open(dir string) (*Store, *model.Error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, &model.Error{Code: model.ErrInput, Msg: err.Error()}
	}
	s := &Store{dir: dir}
	mp4Path, _ := SafeJoin(dir, "index.mp4")
	cpPath, _ := SafeJoin(dir, "checkpoint.json")
	mp4Data, mp4Err := os.ReadFile(mp4Path)
	cpData, cpErr := os.ReadFile(cpPath)
	if mp4Err == nil && cpErr == nil {
		boxes, err := mp4.ExtractBoxWithPayload(bytes.NewReader(mp4Data), nil, mp4.BoxPath{mp4.StrToBoxType("mrci")})
		if err != nil || len(boxes) != 1 {
			return nil, &model.Error{Code: model.ErrArchive, Msg: "index box unreadable"}
		}
		payload, ok := boxes[0].Payload.(*mrciBox)
		if !ok {
			return nil, &model.Error{Code: model.ErrArchive, Msg: "unexpected box payload"}
		}
		if err := json.Unmarshal(payload.Data, &s.entries); err != nil {
			return nil, &model.Error{Code: model.ErrArchive, Msg: "index payload corrupt"}
		}
		var cp checkpoint
		if err := json.Unmarshal(cpData, &cp); err != nil {
			return nil, &model.Error{Code: model.ErrArchive, Msg: "checkpoint corrupt"}
		}
		if cp.Count != len(s.entries) || cp.SHA1 != digest(s.entries) {
			return nil, &model.Error{Code: model.ErrArchive, Msg: "checkpoint does not match committed index"}
		}
	} else if mp4Err == nil || cpErr == nil {
		return nil, &model.Error{Code: model.ErrArchive, Msg: "partial archive state"}
	}
	// Drop stale journals from interrupted transactions (rolled back).
	stale, _ := filepath.Glob(filepath.Join(dir, "journal-*.json"))
	for _, j := range stale {
		os.Remove(j)
	}
	return s, nil
}

// Entries returns the committed entries.
func (s *Store) Entries() []Entry { return append([]Entry(nil), s.entries...) }

// Rollbacks returns how many transactions were rolled back.
func (s *Store) Rollbacks() int { return s.rollbacks }

// Begin starts a transaction for one version.
func (s *Store) Begin(version string) *Txn { return &Txn{store: s, version: version} }

// Txn is a pending per-version transaction.
type Txn struct {
	store   *Store
	version string
	pending []Entry
}

// Add stages an entry.
func (t *Txn) Add(e Entry) { t.pending = append(t.pending, e) }

func (s *Store) writeLocked() *model.Error {
	payload, err := json.Marshal(s.entries)
	if err != nil {
		return &model.Error{Code: model.ErrArchive, Msg: err.Error()}
	}
	tmp, _ := SafeJoin(s.dir, "index.mp4.tmp")
	dst, _ := SafeJoin(s.dir, "index.mp4")
	f, err := os.Create(tmp)
	if err != nil {
		return &model.Error{Code: model.ErrArchive, Msg: err.Error()}
	}
	w := mp4.NewWriter(f)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mrci")}); err == nil {
		_, err = mp4.Marshal(w, &mrciBox{Data: payload}, mp4.Context{})
	}
	if err == nil {
		_, err = w.EndBox()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return &model.Error{Code: model.ErrArchive, Msg: err.Error()}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return &model.Error{Code: model.ErrArchive, Msg: err.Error()}
	}
	cp, _ := json.Marshal(checkpoint{Count: len(s.entries), SHA1: digest(s.entries)})
	cpPath, _ := SafeJoin(s.dir, "checkpoint.json")
	if err := os.WriteFile(cpPath, cp, 0o644); err != nil {
		return &model.Error{Code: model.ErrArchive, Msg: err.Error()}
	}
	return nil
}

// Commit writes the journal, then atomically commits pending entries and
// updates the checkpoint.
func (t *Txn) Commit() *model.Error {
	journal, jerr := SafeJoin(t.store.dir, "journal-"+t.version+".json")
	if jerr != nil {
		return jerr
	}
	data, err := json.Marshal(t.pending)
	if err != nil {
		return &model.Error{Code: model.ErrArchive, Msg: err.Error()}
	}
	if err := os.WriteFile(journal, data, 0o644); err != nil {
		return &model.Error{Code: model.ErrArchive, Msg: err.Error()}
	}
	t.store.entries = append(t.store.entries, t.pending...)
	if merr := t.store.writeLocked(); merr != nil {
		t.store.entries = t.store.entries[:len(t.store.entries)-len(t.pending)]
		return merr
	}
	os.Remove(journal)
	return nil
}

// Rollback discards the transaction; committed entries stay recoverable.
func (t *Txn) Rollback() *model.Error {
	t.store.rollbacks++
	journal, jerr := SafeJoin(t.store.dir, "journal-"+t.version+".json")
	if jerr != nil {
		return jerr
	}
	os.Remove(journal)
	return nil
}
