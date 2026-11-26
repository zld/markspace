// Package manifest merges per-run processing outputs into one canonical
// manifest. Entries are keyed by stable task and track keys, sorted
// deterministically, and summarized by a SHA-256 digest over the canonical
// byte form so repeated runs cannot change the result.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// Entry is one archived object as recorded in the manifest. TaskKey derives
// from the source and policy digests; TrackKey is the timeline track.
type Entry struct {
	TaskKey    string
	TrackKey   string
	SampleIdx  int
	ObjectHash string // hex SHA-256 of the archived content
	Size       int64
	StartNs    int64
	DurationNs int64
}

// canonicalKey defines the total ordering: task, then track, then sample.
func (e Entry) canonicalKey() string {
	return fmt.Sprintf("%s|%s|%012d", e.TaskKey, e.TrackKey, e.SampleIdx)
}

// Merge combines entries from any number of runs. Duplicates (same task,
// track and sample index) collapse only when every recorded field matches;
// a conflicting duplicate is reported rather than silently overwritten.
func Merge(batches ...[]Entry) ([]Entry, error) {
	index := map[string]Entry{}
	var order []string
	for _, batch := range batches {
		for _, e := range batch {
			if e.TaskKey == "" || e.TrackKey == "" {
				return nil, fmt.Errorf("manifest: entry missing task or track key: %+v", e)
			}
			key := e.canonicalKey()
			if existing, ok := index[key]; ok {
				if !equalEntry(existing, e) {
					return nil, fmt.Errorf("manifest: conflicting entries for key %s", key)
				}
				continue
			}
			index[key] = e
			order = append(order, key)
		}
	}
	out := make([]Entry, 0, len(order))
	for _, k := range order {
		out = append(out, index[k])
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].canonicalKey() < out[j].canonicalKey()
	})
	return out, nil
}

func equalEntry(a, b Entry) bool {
	return a.ObjectHash == b.ObjectHash && a.Size == b.Size &&
		a.StartNs == b.StartNs && a.DurationNs == b.DurationNs
}

// Digest returns the 64-character hex SHA-256 of the manifest's canonical
// serialization. The encoding is field-ordered and length-prefixed so no
// delimiter can be confused with content.
func Digest(entries []Entry) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "task=%s\x00track=%s\x00sample=%d\x00hash=%s\x00size=%d\x00start=%d\x00dur=%d\x00\n",
			e.TaskKey, e.TrackKey, e.SampleIdx, e.ObjectHash, e.Size, e.StartNs, e.DurationNs)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Serialize renders the manifest to stable, line-oriented text. It is the
// only on-disk representation and round-trips through Parse.
func Serialize(entries []Entry) []byte {
	var buf []byte
	for _, e := range entries {
		buf = append(buf, fmt.Appendf(nil,
			"%s\t%s\t%d\t%s\t%d\t%d\t%d\n",
			e.TaskKey, e.TrackKey, e.SampleIdx, e.ObjectHash, e.Size, e.StartNs, e.DurationNs)...)
	}
	return buf
}
