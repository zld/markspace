package model

import "sort"

// IndexEntry is one random-access point into the repaired fMP4. Offsets are
// absolute byte positions in the delivered file; every offset must land on a
// valid box boundary.
type IndexEntry struct {
	TrackID      uint32 `json:"track_id"`
	Sequence     uint32 `json:"sequence"`
	MoofOffset   uint64 `json:"moof_offset"`
	MdatOffset   uint64 `json:"mdat_offset"`
	StartMicros  Micros `json:"start_us"`
	EndMicros    Micros `json:"end_us"`
	FirstSample  uint32 `json:"first_sample"`
	SampleCount  uint32 `json:"sample_count"`
	RandomAccess bool   `json:"random_access"`
	SyncSample   uint32 `json:"sync_sample"`
}

// Index is the rebuilt random-access index. Boundaries records every box
// boundary the offsets were validated against so a verifier can recheck.
type Index struct {
	Entries    []IndexEntry `json:"entries"`
	Boundaries []ByteRange  `json:"boundaries"`
	TotalBytes uint64       `json:"total_bytes"`
}

// Len returns the number of index entries.
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.Entries)
}

// Sorted returns entries ordered per track by time, then by offset.
func (ix *Index) Sorted() []IndexEntry {
	out := append([]IndexEntry(nil), ix.Entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TrackID != out[j].TrackID {
			return out[i].TrackID < out[j].TrackID
		}
		if out[i].StartMicros != out[j].StartMicros {
			return out[i].StartMicros < out[j].StartMicros
		}
		return out[i].MoofOffset < out[j].MoofOffset
	})
	return out
}

// TrackSpan returns the earliest start and latest end microsecond for a track.
func (ix *Index) TrackSpan(trackID uint32) (Micros, Micros, bool) {
	var first, last Micros
	found := false
	for _, e := range ix.Entries {
		if e.TrackID != trackID {
			continue
		}
		if !found || e.StartMicros < first {
			first = e.StartMicros
		}
		if !found || e.EndMicros > last {
			last = e.EndMicros
		}
		found = true
	}
	return first, last, found
}

// RepairRecord documents a repaired tfdt or mfhd anomaly: the original
// value, the repaired value and the source byte range it came from.
type RepairRecord struct {
	Kind          string    `json:"kind"`
	TrackID       uint32    `json:"track_id"`
	Sequence      uint32    `json:"sequence"`
	OriginalValue uint64    `json:"original_value"`
	RepairedValue uint64    `json:"repaired_value"`
	SourceRange   ByteRange `json:"source_range"`
	Detail        string    `json:"detail,omitempty"`
}

// BadFragment records an isolated fragment that could not be delivered.
type BadFragment struct {
	Index       int       `json:"index"`
	Sequence    uint32    `json:"sequence,omitempty"`
	TrackID     uint32    `json:"track_id,omitempty"`
	Range       ByteRange `json:"range"`
	Category    Category  `json:"category"`
	Detail      string    `json:"detail,omitempty"`
	Recovered   bool      `json:"recovered"`
	RecoveredAt int       `json:"recovered_at,omitempty"`
}

// TrackManifest is the per-track summary in the delivery manifest.
type TrackManifest struct {
	TrackID        uint32 `json:"track_id"`
	Timescale      uint32 `json:"timescale"`
	Handler        string `json:"handler,omitempty"`
	Codec          string `json:"codec,omitempty"`
	StartMicros    Micros `json:"start_us"`
	EndMicros      Micros `json:"end_us"`
	DurationMicros Micros `json:"duration_us"`
	Samples        int    `json:"samples"`
	SyncSamples    int    `json:"sync_samples"`
}

// Manifest is the machine-readable delivery record. It lists per-track
// spans, repairs and bad fragments and carries the metrics block. It is
// serialised deterministically: maps are avoided, slices are sorted and the
// digest is computed over the canonical JSON of the payload.
type Manifest struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	// ManifestSHA256 is the digest of the canonical manifest payload with this
	// field cleared. verify recomputes it to detect tampering.
	ManifestSHA256      string          `json:"manifest_sha256"`
	Seed                int64           `json:"seed"`
	OutputSHA256        string          `json:"sha256"`
	OutputSize          uint64          `json:"output_bytes"`
	SourceBytes         uint64          `json:"source_bytes"`
	OutputPath          string          `json:"output_path"`
	IndexPath           string          `json:"index_path,omitempty"`
	TargetBPS           int64           `json:"target_bps"`
	ActualBPS           int64           `json:"actual_bps"`
	BitrateDeltaPct     float64         `json:"bitrate_delta_pct"`
	ValidSamplePct      float64         `json:"valid_sample_pct"`
	DroppedFragments    int             `json:"dropped_fragments"`
	ManifestAlignmentUS Micros          `json:"manifest_alignment_us"`
	IndexEntries        int             `json:"index_entries"`
	RepairedTfdt        int             `json:"repaired_tfdt"`
	MFHDGaps            int             `json:"mfhd_gaps"`
	TruncatedBoxes      int             `json:"truncated_boxes"`
	RecoveredFragments  int             `json:"recovered_fragments"`
	DuplicateSequences  int             `json:"duplicate_sequences"`
	TotalSamples        int             `json:"total_samples"`
	ValidSamples        int             `json:"valid_samples"`
	DurationMicros      Micros          `json:"duration_us"`
	Tracks              []TrackManifest `json:"tracks"`
	Repairs             []RepairRecord  `json:"repairs"`
	BadFragments        []BadFragment   `json:"bad_fragments"`
	RTP                 *RTPReport      `json:"rtp,omitempty"`
	DamagedIndex        bool            `json:"damaged_index"`
	IndexIssues         []Issue         `json:"index_issues,omitempty"`
	Index               *Index          `json:"-"`
}

// RTPReport summarises RTP timestamp wrap validation.
type RTPReport struct {
	Packets       int    `json:"packets"`
	Wraps         int    `json:"wraps"`
	ClockRate     uint32 `json:"clock_rate"`
	FirstMicros   Micros `json:"first_us"`
	LastMicros    Micros `json:"last_us"`
	MaxSkewMicros Micros `json:"max_skew_us"`
	Matched       bool   `json:"matched"`
}
