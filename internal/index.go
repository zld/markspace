package medpipe

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// IndexSchemaVersion is bumped whenever the on-disk index format changes.
const IndexSchemaVersion = 1

// IndexEntry is one random-access point: a keyframe plus the byte position of
// its transport record and its normalized presentation time.
type IndexEntry struct {
	Ordinal    int    `json:"ordinal"` // frame ordinal within the track
	Keyframe   bool   `json:"keyframe"`
	ByteOffset int64  `json:"byte_offset"` // absolute offset in the capture
	UnifiedUS  int64  `json:"unified_us"`  // presentation time, unified clock
	OriginalTS uint32 `json:"original_ts"` // raw RTP timestamp
	RepairedTS uint64 `json:"repaired_ts"` // monotonic repaired timestamp
	Size       int    `json:"size"`        // coded frame size in bytes
	// Meta is the in-band metadata attached at scan time (color triple and
	// quality marker), persisted so validation runs from the index alone.
	Meta FrameMeta `json:"meta"`
}

// TrackIndex is the rebuilt random-access index for one capture, bound to the
// exact input digest so any content change invalidates it.
type TrackIndex struct {
	SchemaVersion int          `json:"schema_version"`
	Track         string       `json:"track"`
	Codec         Codec        `json:"codec"`
	Kind          Kind         `json:"kind"`
	ClockRate     uint32       `json:"clock_rate"`
	Input         Digest       `json:"input_digest"`
	FrameCount    int          `json:"frame_count"`
	KeyframeCount int          `json:"keyframe_count"`
	DurationUS    int64        `json:"duration_us"`
	ClockStats    ClockStats   `json:"clock_stats"`
	Entries       []IndexEntry `json:"entries"`
	// ResumeOrdinal is the next frame ordinal the streaming stage should
	// process; an interrupted run resumes at the preceding keyframe.
	ResumeOrdinal int `json:"resume_ordinal"`
}

// IndexFileName is the conventional sidecar name under a run directory.
func IndexFileName(track string) string { return track + ".index.json" }

// BuildIndex converts scan units into random-access entries. Only keyframes
// become seek points; every frame ordinal is retained so resume can locate
// the nearest confirmed keyframe before an interruption point.
func BuildIndex(track string, codec Codec, kind Kind, clockRate uint32, input Digest, scan ScanResult) TrackIndex {
	idx := TrackIndex{
		SchemaVersion: IndexSchemaVersion,
		Track:         track,
		Codec:         codec,
		Kind:          kind,
		ClockRate:     clockRate,
		Input:         input,
		ClockStats:    scan.ClockStats,
	}
	for i, u := range scan.Units {
		e := IndexEntry{
			Ordinal:    i,
			Keyframe:   u.Frame.Keyframe,
			ByteOffset: u.Frame.ByteOffset,
			UnifiedUS:  u.Sample.UnifiedUS,
			OriginalTS: u.Sample.OriginalTS,
			RepairedTS: u.Sample.RepairedTS,
			Size:       len(u.Frame.RawPayload),
			Meta:       u.Frame.Meta,
		}
		if e.Keyframe {
			idx.KeyframeCount++
		}
		idx.Entries = append(idx.Entries, e)
	}
	idx.FrameCount = len(idx.Entries)
	if idx.FrameCount > 0 {
		idx.DurationUS = idx.Entries[idx.FrameCount-1].UnifiedUS - idx.Entries[0].UnifiedUS
	}
	return idx
}

// Save writes the index as an atomic JSON sidecar after validating its path.
func (idx *TrackIndex) Save(baseDir string) error {
	name, perr := ResolveLocal(baseDir, IndexFileName(idx.Track))
	if perr != nil {
		return perr
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(name, data, 0o644)
}

// LoadIndex reads and validates a sidecar index.
func LoadIndex(baseDir, track string) (*TrackIndex, error) {
	name, perr := ResolveLocal(baseDir, IndexFileName(track))
	if perr != nil {
		return nil, perr
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var idx TrackIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("index %s corrupt: %w", name, err)
	}
	if idx.SchemaVersion != IndexSchemaVersion {
		return nil, fmt.Errorf("index %s schema version %d, want %d", name, idx.SchemaVersion, IndexSchemaVersion)
	}
	if !sort.SliceIsSorted(idx.Entries, func(i, j int) bool {
		return idx.Entries[i].UnifiedUS < idx.Entries[j].UnifiedUS
	}) {
		return nil, fmt.Errorf("index %s entries not in monotonic time order", name)
	}
	return &idx, nil
}

// IsValidFor reports whether the index may be reused for the given input:
// schema, track identity and content digest must all match.
func (idx *TrackIndex) IsValidFor(track string, input Digest) bool {
	return idx != nil &&
		idx.SchemaVersion == IndexSchemaVersion &&
		idx.Track == track &&
		idx.Input.Equal(input)
}

// KeyframeAtOrBefore returns the seek entry with the greatest ordinal not
// exceeding ordinal. The first frame is returned when nothing precedes it.
func (idx *TrackIndex) KeyframeAtOrBefore(ordinal int) (IndexEntry, bool) {
	if len(idx.Entries) == 0 {
		return IndexEntry{}, false
	}
	if ordinal >= len(idx.Entries) {
		ordinal = len(idx.Entries) - 1
	}
	for i := ordinal; i >= 0; i-- {
		if idx.Entries[i].Keyframe {
			return idx.Entries[i], true
		}
	}
	return idx.Entries[0], true
}

// Keyframes returns only the random-access entries.
func (idx *TrackIndex) Keyframes() []IndexEntry {
	var out []IndexEntry
	for _, e := range idx.Entries {
		if e.Keyframe {
			out = append(out, e)
		}
	}
	return out
}
