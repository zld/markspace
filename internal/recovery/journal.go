// Package recovery implements write-ahead publish journaling and restart repair.
package recovery

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"mediapipeline/internal/evidence"
)

// Event kinds stored as JSON Lines.
const (
	EventPrepared  = "prepared"
	EventCommitted = "committed"
)

// Intent is a write-ahead publish record.
type Intent struct {
	Event      string    `json:"event"`
	ID         string    `json:"id"`
	Key        string    `json:"key"`
	SourcePath string    `json:"source_path,omitempty"`
	TempPath   string    `json:"temp_path"`
	TargetPath string    `json:"target_path"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
}

// Stats summarizes restart recovery.
type Stats struct {
	Recovered      int      `json:"recovered"`
	Republished    int      `json:"republished"`
	Missing        int      `json:"missing"`
	Corrupt        int      `json:"corrupt"`
	Duplicates     int      `json:"duplicates"`
	NeedsWork      []Intent `json:"-"`
	RecoveredItems []Intent `json:"-"`
	MissingItems   []Intent `json:"-"`
	CorruptItems   []Intent `json:"-"`
}

// Journal is an append-only, fsynced JSONL publish log.
type Journal struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// Open opens or creates the journal file.
func Open(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Journal{path: path, f: f}, nil
}

// Close syncs and closes the journal.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	if err := j.f.Sync(); err != nil {
		return err
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// Prepare appends and durably stores a pre-publish intent.
func (j *Journal) Prepare(in Intent) (Intent, error) {
	if in.Key == "" || in.TargetPath == "" || in.SHA256 == "" {
		return in, errors.New("recovery: intent requires key, target and sha256")
	}
	if in.ID == "" {
		in.ID = evidence.SHA256Bytes([]byte(fmt.Sprintf("%s|%s|%d", in.Key, in.TempPath, time.Now().UnixNano())))[:16]
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	in.Event = EventPrepared
	if err := j.append(in); err != nil {
		return in, err
	}
	return in, nil
}

// Commit records that an intent was completed and index state reconciled.
func (j *Journal) Commit(id string) error {
	return j.append(struct {
		Event     string    `json:"event"`
		ID        string    `json:"id"`
		Committed time.Time `json:"committed_at"`
	}{Event: EventCommitted, ID: id, Committed: time.Now().UTC()})
}

func (j *Journal) append(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(b); err != nil {
		return err
	}
	return j.f.Sync()
}

// ReadAll returns all journal records in append order.
func (j *Journal) ReadAll() ([]Intent, map[string]bool, error) {
	f, err := os.Open(j.path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return Read(f)
}

// Read parses a JSONL journal stream. Intent records are returned; committed
// IDs are collected separately.
func Read(r io.Reader) ([]Intent, map[string]bool, error) {
	var intents []Intent
	committed := make(map[string]bool)
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var head struct {
			Event string `json:"event"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			return nil, nil, fmt.Errorf("recovery: bad journal line: %w", err)
		}
		switch head.Event {
		case EventPrepared:
			var in Intent
			if err := json.Unmarshal(line, &in); err != nil {
				return nil, nil, err
			}
			intents = append(intents, in)
		case EventCommitted:
			committed[head.ID] = true
		}
	}
	return intents, committed, s.Err()
}

// Recover verifies uncommitted published targets. Valid targets are counted as
// recovered; missing or corrupt targets are returned for deterministic redo.
func (j *Journal) Recover() (Stats, error) {
	intents, committed, err := j.ReadAll()
	if err != nil {
		return Stats{}, err
	}
	var stats Stats
	latestByKey := make(map[string]Intent)
	seenKey := make(map[string]int)
	for _, in := range intents {
		if committed[in.ID] {
			continue
		}
		seenKey[in.Key]++
		latestByKey[in.Key] = in
	}
	keys := make([]string, 0, len(latestByKey))
	for k := range latestByKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if seenKey[key] > 1 {
			stats.Duplicates += seenKey[key] - 1
		}
		in := latestByKey[key]
		got, size, err := evidence.SHA256File(in.TargetPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
			stats.Missing++
			stats.MissingItems = append(stats.MissingItems, in)
			stats.NeedsWork = append(stats.NeedsWork, in)
		case err != nil:
			stats.Corrupt++
			stats.CorruptItems = append(stats.CorruptItems, in)
			stats.NeedsWork = append(stats.NeedsWork, in)
		case got != in.SHA256 || (in.Size != 0 && size != in.Size):
			stats.Corrupt++
			stats.CorruptItems = append(stats.CorruptItems, in)
			stats.NeedsWork = append(stats.NeedsWork, in)
		default:
			stats.Recovered++
			stats.RecoveredItems = append(stats.RecoveredItems, in)
			if err := j.Commit(in.ID); err != nil {
				return stats, err
			}
		}
	}
	return stats, nil
}

// Quarantine moves a corrupt published artifact aside and returns its new path.
func Quarantine(target string) (string, error) {
	dir := filepath.Join(filepath.Dir(target), "quarantine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s.%d.corrupt", filepath.Base(target), time.Now().UnixNano()))
	if err := os.Rename(target, dst); err != nil {
		return "", err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return dst, nil
}
