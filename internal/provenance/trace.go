// Package provenance records how every output segment traces back to its
// input fragments and lets repeated runs prove they produced identical work.
//
// A Trace is a deterministic, JSON-serializable record: it contains one entry
// per committed segment, each listing the input fragment digests that fed it.
// Because ordering and hashing are fixed, the same inputs always yield the
// same trace SHA-256, and Diff reports exactly which fields changed between
// a retry and the previous successful run.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// InputRef identifies one source fragment and its digest.
type InputRef struct {
	InputPath  string `json:"input_path"`
	SHA256     string `json:"sha256"`
	MFHD       uint32 `json:"mfhd"`
	TFDT       uint64 `json:"tfdt"`
}

// Entry is one committed segment's lineage record.
type Entry struct {
	SegmentIndex int        `json:"segment_index"`
	StartNanos   int64      `json:"start_nanos"`
	EndNanos     int64      `json:"end_nanos"`
	Inputs       []InputRef `json:"inputs"`
}

// Trace is the full provenance record of one pipeline run.
type Trace struct {
	// ManifestVersion links the trace to a manifest generation.
	ManifestVersion string  `json:"manifest_version"`
	Entries         []Entry `json:"entries"`
}

// New creates an empty trace bound to a manifest version.
func New(manifestVersion string) *Trace {
	return &Trace{ManifestVersion: manifestVersion, Entries: []Entry{}}
}

// Add appends one segment entry. Input refs are sorted by path so equal
// logical records always serialize identically.
func (t *Trace) Add(e Entry) {
	sort.Slice(e.Inputs, func(i, j int) bool {
		return e.Inputs[i].InputPath < e.Inputs[j].InputPath
	})
	t.Entries = append(t.Entries, e)
}

// canonicalJSON returns deterministic JSON: struct fields already follow a
// fixed declaration order, and map iteration is never used.
func (t *Trace) canonicalJSON() ([]byte, error) {
	return json.MarshalIndent(t, "", "  ")
}

// SHA256 returns the hex digest of the canonical trace JSON.
func (t *Trace) SHA256() (string, error) {
	data, err := t.canonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// WriteJSON writes the canonical trace to path.
func (t *Trace) WriteJSON(path string) error {
	data, err := t.canonicalJSON()
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// ReadJSON loads a trace previously written with WriteJSON.
func ReadJSON(path string) (*Trace, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	var t Trace
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// FieldDiff names one differing leaf between two trace entries.
type FieldDiff struct {
	SegmentIndex int    `json:"segment_index"`
	Field        string `json:"field"`
	Previous     string `json:"previous"`
	Current      string `json:"current"`
}

// Diff compares two traces and returns every differing field. A zero-length
// result means the runs are byte-for-byte equivalent at the record level.
// Entry order is significant; a missing or extra segment is reported once.
func Diff(previous, current *Trace) []FieldDiff {
	var diffs []FieldDiff
	maxLen := len(previous.Entries)
	if len(current.Entries) > maxLen {
		maxLen = len(current.Entries)
	}
	for i := 0; i < maxLen; i++ {
		switch {
		case i >= len(previous.Entries):
			diffs = append(diffs, FieldDiff{SegmentIndex: i, Field: "entry", Previous: "<absent>", Current: "present"})
			continue
		case i >= len(current.Entries):
			diffs = append(diffs, FieldDiff{SegmentIndex: i, Field: "entry", Previous: "present", Current: "<absent>"})
			continue
		}
		a, b := previous.Entries[i], current.Entries[i]
		if a.StartNanos != b.StartNanos {
			diffs = append(diffs, FieldDiff{i, "start_nanos", itoa(a.StartNanos), itoa(b.StartNanos)})
		}
		if a.EndNanos != b.EndNanos {
			diffs = append(diffs, FieldDiff{i, "end_nanos", itoa(a.EndNanos), itoa(b.EndNanos)})
		}
		diffs = append(diffs, diffInputs(i, a.Inputs, b.Inputs)...)
	}
	if previous.ManifestVersion != current.ManifestVersion {
		diffs = append(diffs, FieldDiff{Field: "manifest_version", Previous: previous.ManifestVersion, Current: current.ManifestVersion})
	}
	return diffs
}

func diffInputs(segIdx int, prev, cur []InputRef) []FieldDiff {
	var diffs []FieldDiff
	maxLen := len(prev)
	if len(cur) > maxLen {
		maxLen = len(cur)
	}
	for i := 0; i < maxLen; i++ {
		switch {
		case i >= len(prev):
			diffs = append(diffs, FieldDiff{segIdx, "inputs", "<absent>", cur[i].InputPath})
			continue
		case i >= len(cur):
			diffs = append(diffs, FieldDiff{segIdx, "inputs", prev[i].InputPath, "<absent>"})
			continue
		}
		if prev[i] != cur[i] {
			diffs = append(diffs, FieldDiff{segIdx, "inputs[" + prev[i].InputPath + "]",
				prev[i].SHA256, cur[i].SHA256})
		}
	}
	return diffs
}
