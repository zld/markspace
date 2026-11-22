package container

import (
	"archivist/internal/model"
)

// ParseOptions controls recovery behavior of ParseContainer.
type ParseOptions struct {
	// Data is the raw container byte source, required for stss rebuilding.
	Data []byte
	// AllowTruncatedRecovery trims dangling samples whose chunks run past EOF
	// instead of rejecting the whole container.
	AllowTruncatedRecovery bool
}

// ParsedFile is the normalized container view.
type ParsedFile struct {
	MovieTimescale uint32
	MovieDuration  uint64
	Brand          string
	Tracks         []*model.Track
	// Recovery notes which structural repairs were performed.
	Recovery []RecoveryNote
	Warnings []model.Warning
	Root     *ParseResult
}

// RecoveryNote names a rebuilt index or trimmed tail for reporting.
type RecoveryNote struct {
	TrackID uint32
	Kind    string // "truncated-box" or "rebuilt-keyframe-index"
	Detail  string
}

// chunkRun is one stsc entry: samples-per-chunk applies from firstChunk.
type chunkRun struct {
	firstChunk uint32
	samples    uint32
}

func appendMode(mode, suffix string) string {
	if mode == "" {
		return suffix
	}
	return mode + "+" + suffix
}

func containsNote(mode, note string) bool {
	for i := 0; i+len(note) <= len(mode); i++ {
		if mode[i:i+len(note)] == note {
			return true
		}
	}
	return false
}

// decodeLanguage unpacks the packed ISO 639-2/T language code.
func decodeLanguage(v uint16) string {
	a := byte((v>>10)&0x1F) + 0x60
	b := byte((v>>5)&0x1F) + 0x60
	c := byte(v&0x1F) + 0x60
	if a < 0x61 || b < 0x61 || c < 0x61 {
		return "und"
	}
	return string([]byte{a, b, c})
}
