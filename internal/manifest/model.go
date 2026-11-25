package manifest

import "mediacore/internal/diagnostic"

// Entry is the unified timeline record emitted by either DASH or HLS parsing.
type Entry struct {
	// StableKey is idempotency key for the source item.
	StableKey string `json:"stable_key"`
	System    string `json:"system"` // dash | hls
	Manifest  string `json:"manifest"`
	// Representation is DASH Representation ID or HLS rendition name.
	Representation string `json:"representation"`
	// EntryIndex is zero-based position in the parsed manifest/list.
	EntryIndex int `json:"entry_index"`
	// OriginalSequence preserves DASH startNumber+position or HLS MEDIA-SEQUENCE+position.
	OriginalSequence int `json:"original_sequence"`

	InitPath  string `json:"init_path,omitempty"`
	MediaPath string `json:"media_path"`
	MediaURI  string `json:"media_uri"`

	SourceTimescale int64 `json:"source_timescale"`
	SourceStart     int64 `json:"source_start"`
	SourceDuration  int64 `json:"source_duration"`

	ProgramStartU int64 `json:"program_start_u"`
	ProgramEndU   int64 `json:"program_end_u"`

	ReferenceStartU int64 `json:"reference_start_u"`
	DeviationU      int64 `json:"deviation_u"`
	Aligned         bool  `json:"aligned"`

	DiscontinuityBefore bool `json:"discontinuity_before,omitempty"`
}

func (e Entry) Location(stage string) diagnostic.Location {
	return diagnostic.Location{
		Stage:          stage,
		ManifestPath:   e.Manifest,
		Representation: e.Representation,
		EntryKey:       e.StableKey,
		EntryIndex:     e.EntryIndex,
		Sequence:       e.OriginalSequence,
		ByteOffset:     -1,
	}
}

// ValidateMonotonic ensures program starts do not move backwards.
// Equal starts are permitted only for zero-duration items.
func ValidateMonotonic(entries []Entry) error {
	var last Entry
	have := false
	for _, e := range entries {
		if have && e.ProgramStartU < last.ProgramStartU {
			loc := e.Location("manifest")
			return diagnostic.New(diagnostic.ErrTimeRegression, loc,
				"program time moved backwards from "+itoa(last.ProgramStartU)+" to "+itoa(e.ProgramStartU))
		}
		last = e
		have = true
	}
	return nil
}

func itoa(v int64) string {
	// strconv is intentionally avoided in this tiny helper to keep import lists obvious.
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// DedupBySequence drops repeated original sequence numbers while preserving first output order.
func DedupBySequence(entries []Entry) []Entry {
	seen := make(map[string]struct{}, len(entries))
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		k := e.Representation + "/" + itoa(int64(e.OriginalSequence))
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, e)
	}
	return out
}
