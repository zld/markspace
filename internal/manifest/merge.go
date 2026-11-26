// Package manifest merges sealed task outputs into one deterministic
// manifest. Entries are sorted by time, spec and stable task key; the
// whole document is canonical JSON so repeated merges of identical
// inputs produce byte-identical output (identical SHA-256). A task key
// already present with different content is a conflict, never an
// overwrite.
package manifest

import (
	"audionorm/internal/audio"
	"audionorm/internal/media"
	"audionorm/internal/task"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
)

// Entry is one sealed task as it appears in the manifest.
type Entry struct {
	TaskKey        string                `json:"task_key"`
	StreamID       string                `json:"stream_id"`
	Start          uint64                `json:"start"`
	End            uint64                `json:"end"`
	Spec           task.Spec             `json:"spec"`
	Status         task.Status           `json:"status"`
	Samples        int                   `json:"samples"`
	MissingSamples uint64                `json:"missing_samples"`
	BudgetDevPct   float64               `json:"budget_deviation_pct"`
	OutputSHA256   string                `json:"output_sha256"`
	Loudness       *audio.LoudnessReport `json:"loudness,omitempty"`
	Errors         []*media.MediaError   `json:"errors,omitempty"`
}

// Document is the canonical, versioned manifest.
type Document struct {
	SchemaVersion string  `json:"schema_version"`
	Entries       []Entry `json:"entries"`
}

// SchemaVersion is bumped on any incompatible manifest format change.
const SchemaVersion = "audionorm-manifest-1"

// ErrConflict indicates an existing entry has the same key but
// different content.
var ErrConflict = errors.New("manifest entry conflict")

// FromCheckpoint projects a sealed checkpoint into a manifest entry.
// outputSHA is the hash of the sealed output payload.
func FromCheckpoint(cp *task.Checkpoint, outputSHA string) Entry {
	return Entry{
		TaskKey: cp.TaskKey, StreamID: cp.StreamID,
		Start: cp.Range.Start, End: cp.Range.End, Spec: cp.Spec,
		Status: cp.Status, Samples: cp.Samples,
		MissingSamples: cp.MissingSamples, BudgetDevPct: cp.BudgetDevPct,
		OutputSHA256: outputSHA, Loudness: cp.Loudness, Errors: cp.Errors,
	}
}

// sortEntries orders by start time, then spec (rate, channels, rms,
// peak), then the stable task key for fully deterministic output.
func sortEntries(e []Entry) {
	sort.Slice(e, func(i, j int) bool {
		a, b := e[i], e[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End != b.End {
			return a.End < b.End
		}
		if a.Spec != b.Spec {
			if a.Spec.SampleRate != b.Spec.SampleRate {
				return a.Spec.SampleRate < b.Spec.SampleRate
			}
			if a.Spec.Channels != b.Spec.Channels {
				return a.Spec.Channels < b.Spec.Channels
			}
			if a.Spec.TargetRMS != b.Spec.TargetRMS {
				return a.Spec.TargetRMS < b.Spec.TargetRMS
			}
			return a.Spec.PeakCeilDB < b.Spec.PeakCeilDB
		}
		return a.TaskKey < b.TaskKey
	})
}

// Merge folds new entries into an existing document. Identical entries
// (same key and content) are deduplicated silently; a same-key
// different-content entry returns ErrConflict and changes nothing.
func Merge(existing *Document, incoming ...Entry) (*Document, error) {
	if existing == nil {
		existing = &Document{SchemaVersion: SchemaVersion}
	}
	doc := &Document{SchemaVersion: SchemaVersion, Entries: append([]Entry(nil), existing.Entries...)}
	index := map[string]int{}
	for i, e := range doc.Entries {
		index[e.TaskKey] = i
	}
	for _, e := range incoming {
		if pos, ok := index[e.TaskKey]; ok {
			if equalEntry(doc.Entries[pos], e) {
				continue // idempotent re-merge
			}
			return nil, errors.Join(ErrConflict,
				errors.New("key "+e.TaskKey+" exists with different content"))
		}
		index[e.TaskKey] = len(doc.Entries)
		doc.Entries = append(doc.Entries, e)
	}
	sortEntries(doc.Entries)
	return doc, nil
}

func equalEntry(a, b Entry) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// Serialize renders the document as canonical, deterministic JSON.
func Serialize(doc *Document) ([]byte, error) {
	sortEntries(doc.Entries)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Hash returns the SHA-256 of the serialized document.
func Hash(doc *Document) (string, error) {
	data, err := Serialize(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// WriteFile atomically writes the serialized manifest to path and
// returns its hash. It refuses to overwrite through a symlink.
func WriteFile(path string, doc *Document) (string, error) {
	data, err := Serialize(doc)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("refusing to write manifest through symlink")
	}
	final, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer final.Close()
	if _, err := final.Write(data); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
