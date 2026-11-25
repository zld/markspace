package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"regress-kernel/internal/media"
)

// SegmentEntry is one manifest record, keyed by stable task and start time.
type SegmentEntry struct {
	TaskKey   string `json:"task_key"`
	Rendition string `json:"rendition"`
	Name      string `json:"name"`
	StartTick int64  `json:"start_tick"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// Manifest is the deterministic, byte-stable multi-bitrate program index.
type Manifest struct {
	CaseName string         `json:"case_name"`
	Seed     int64          `json:"seed"`
	Entries  []SegmentEntry `json:"entries"`
}

// entryKey identifies one unique segment position across redeliveries.
type entryKey struct {
	taskKey   string
	startTick int64
}

// MergeManifest folds one or more deliveries of the same tasks into a single
// deterministic manifest. Sorting is by stable task key then fragment start
// time, so arrival order never affects output. Duplicate records with the
// same key and digest collapse into one; a reused key with a different digest
// is a hard manifest conflict rather than a silent overwrite.
func MergeManifest(caseName string, seed int64,
	deliveries ...[]SegmentEntry) (Manifest, error) {
	merged := map[entryKey]SegmentEntry{}
	order := make([]entryKey, 0)
	for _, delivery := range deliveries {
		for _, e := range delivery {
			k := entryKey{e.TaskKey, e.StartTick}
			if existing, ok := merged[k]; ok {
				if existing.SHA256 != e.SHA256 {
					return Manifest{}, fmt.Errorf(
						"%w: task=%s start=%d digest %s != %s",
						media.ErrManifestConflict, e.TaskKey, e.StartTick,
						existing.SHA256[:12], e.SHA256[:12])
				}
				continue // identical redelivery: keep the single record
			}
			merged[k] = e
			order = append(order, k)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].taskKey != order[j].taskKey {
			return order[i].taskKey < order[j].taskKey
		}
		return order[i].startTick < order[j].startTick
	})
	m := Manifest{CaseName: caseName, Seed: seed,
		Entries: make([]SegmentEntry, 0, len(order))}
	for _, k := range order {
		m.Entries = append(m.Entries, merged[k])
	}
	return m, nil
}

// Marshal renders the manifest with a fixed key order and trailing newline,
// guaranteeing identical bytes for identical input across repeated runs.
func (m Manifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ManifestSHA256 is the content digest of the canonical manifest bytes.
func (m Manifest) ManifestSHA256() (string, error) {
	data, err := m.Marshal()
	if err != nil {
		return "", err
	}
	return FileSHA256(data), nil
}

// EntriesForTask returns the manifest entries of one rendition in order.
func (m Manifest) EntriesForTask(taskKey string) []SegmentEntry {
	var out []SegmentEntry
	for _, e := range m.Entries {
		if e.TaskKey == taskKey {
			out = append(out, e)
		}
	}
	return out
}

// Continuity verifies that a rendition's fragment start times advance by the
// expected constant tick delta with no gaps or overlaps.
func Continuity(entries []SegmentEntry, expectedDelta int64) error {
	for i, e := range entries {
		if i == 0 {
			continue
		}
		if delta := e.StartTick - entries[i-1].StartTick; delta != expectedDelta {
			return fmt.Errorf("%w: task=%s gap=%d want=%d between %s and %s",
				media.ErrCheckpoint, e.TaskKey, delta, expectedDelta,
				entries[i-1].Name, e.Name)
		}
	}
	return nil
}

// EqualManifestBytes is a test helper comparing two serialized manifests.
func EqualManifestBytes(a, b Manifest) (bool, error) {
	ab, err := a.Marshal()
	if err != nil {
		return false, err
	}
	bb, err := b.Marshal()
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}
