// Package manifest merges per-event results into one deterministic document.
//
// Determinism is a hard requirement: the same task key, inputs and event
// list must produce byte-identical JSON on every run, regardless of the
// order in which workers finish. The merger therefore:
//
//   - keys every record by a stable task key and sorts by it;
//   - never embeds timestamps, durations or map iteration order;
//   - writes through a temporary file and renames it into place, so a
//     reader never sees a partial document;
//   - refuses to deliver the same task key twice in one merge.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ErrDuplicate is returned when two records share a task key.
var ErrDuplicate = errors.New("manifest: duplicate task key")

// ErrEmptyKey is returned for records without a task key.
var ErrEmptyKey = errors.New("manifest: record has no task key")

// Clip is one delivered fragment.
type Clip struct {
	TaskKey     string `json:"task_key"`
	Event       string `json:"event"`
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	AudioFrames int64  `json:"audio_frames"`
	AudioPeak   string `json:"audio_peak"`
	VideoFirst  int    `json:"video_first_sample"`
	VideoLast   int    `json:"video_last_sample"`
	LeadInMs    int64  `json:"lead_in_ms"`
	FrameHash   string `json:"frame_hash"`
	PixelMean   string `json:"pixel_mean"`
	Damaged     bool   `json:"damaged"`
}

// Document is the merged output. Field order is fixed by the struct so the
// encoding is stable.
type Document struct {
	Version   int    `json:"version"`
	TaskKey   string `json:"task_key"`
	InputHash string `json:"input_hash"`
	Clips     []Clip `json:"clips"`
	// Hash is filled by Seal; it is not part of the encoded body.
	Hash string `json:"hash"`
}

// Merger accumulates clips until Seal is called.
type Merger struct {
	mu     sync.Mutex
	seen   map[string]struct{}
	clip   map[string]Clip
	key    string
	in     string
	sealed bool
}

// New returns a merger for one task.
func New(taskKey, inputHash string) *Merger {
	return &Merger{
		seen: make(map[string]struct{}),
		clip: make(map[string]Clip),
		key:  taskKey,
		in:   inputHash,
	}
}

// Add merges one clip. Adding the same bytes for an existing key is
// idempotent (a retried worker); adding different bytes for an existing key
// is a duplicate-delivery error.
func (m *Merger) Add(c Clip) error {
	if c.TaskKey == "" {
		return fmt.Errorf("%w: event %q", ErrEmptyKey, c.Event)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sealed {
		return errors.New("manifest: merger already sealed")
	}
	if prev, ok := m.clip[c.TaskKey]; ok {
		if prev == c {
			return nil // identical retry
		}
		return fmt.Errorf("%w: %s (event %q vs %q)", ErrDuplicate, c.TaskKey, prev.Event, c.Event)
	}
	m.clip[c.TaskKey] = c
	return nil
}

// Len returns the number of accepted clips.
func (m *Merger) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.clip)
}

// Seal produces the sorted document and its hash.
func (m *Merger) Seal() (*Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sealed = true
	clips := make([]Clip, 0, len(m.clip))
	for _, c := range m.clip {
		clips = append(clips, c)
	}
	sort.Slice(clips, func(i, j int) bool {
		if clips[i].TaskKey != clips[j].TaskKey {
			return clips[i].TaskKey < clips[j].TaskKey
		}
		return clips[i].Event < clips[j].Event
	})
	doc := &Document{Version: 1, TaskKey: m.key, InputHash: m.in, Clips: clips}
	if doc.Clips == nil {
		doc.Clips = []Clip{}
	}
	body, err := hashBody(doc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	doc.Hash = hex.EncodeToString(sum[:])
	return doc, nil
}

// hashBody encodes the document with its Hash field cleared, the fixed
// basis for both sealing and verification.
func hashBody(doc *Document) ([]byte, error) {
	cp := *doc
	cp.Hash = ""
	return json.Marshal(&cp)
}

// Encode renders a sealed document with a trailing newline and stable
// indentation. Hash matches the compact body, so callers can recompute it.
func Encode(doc *Document) ([]byte, error) {
	body, err := hashBody(doc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if doc.Hash != "" && hex.EncodeToString(sum[:]) != doc.Hash {
		return nil, fmt.Errorf("manifest: document hash does not match contents")
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// WriteAtomic encodes doc and writes it to path via temp+rename.
func WriteAtomic(path string, doc *Document) error {
	data, err := Encode(doc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// HashOf recomputes a document hash from its clips, for verification.
func HashOf(doc *Document) (string, error) {
	body, err := hashBody(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
