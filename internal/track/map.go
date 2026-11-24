// Package track defines track identity and the explicit input-to-output track
// mapping that every remux must present.
package track

import (
	"errors"
	"fmt"
)

// Kind is the semantic type of a track.
type Kind string

const (
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindSubtitle Kind = "subtitle"
	KindChapter  Kind = "chapter"
	KindMetadata Kind = "metadata"
	KindUnknown  Kind = "unknown"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindVideo, KindAudio, KindSubtitle, KindChapter, KindMetadata, KindUnknown:
		return true
	}
	return false
}

// Input describes a demuxed input track.
type Input struct {
	ID        uint32
	Kind      Kind
	Timescale uint32
	Codec     string
}

// Entry preserves the input identity, semantics, time base and the explicit
// output identity of one track.
type Entry struct {
	InputID   uint32
	OutputID  uint32
	Kind      Kind
	Timescale uint32
	Codec     string
}

// Map is an ordered, validated set of track entries.
type Map struct {
	Entries []Entry
}

var (
	// ErrMissingMapping means an input track has no output entry.
	ErrMissingMapping = errors.New("track: missing mapping")
	// ErrDuplicateMapping means an input or output id is used twice.
	ErrDuplicateMapping = errors.New("track: duplicate mapping")
	// ErrInvalidInput means an input track is unusable.
	ErrInvalidInput = errors.New("track: invalid input")
)

// New builds a map from inputs to the supplied output ids.  Both slices must
// have the same length; outputs are assigned positionally.
func New(inputs []Input, outputs []uint32) (Map, error) {
	if len(inputs) != len(outputs) {
		return Map{}, fmt.Errorf("track: %d inputs but %d outputs", len(inputs), len(outputs))
	}
	entries := make([]Entry, 0, len(inputs))
	for i, in := range inputs {
		if in.ID == 0 || in.Timescale == 0 || !in.Kind.Valid() {
			return Map{}, fmt.Errorf("%w: id=%d", ErrInvalidInput, in.ID)
		}
		entries = append(entries, Entry{
			InputID:   in.ID,
			OutputID:  outputs[i],
			Kind:      in.Kind,
			Timescale: in.Timescale,
			Codec:     in.Codec,
		})
	}
	m := Map{Entries: entries}
	if err := m.Validate(); err != nil {
		return Map{}, err
	}
	return m, nil
}

// Identity maps every input to the same output id.  It is convenient for
// fixtures and for the demo, but still goes through full validation.
func Identity(inputs []Input) (Map, error) {
	outputs := make([]uint32, len(inputs))
	for i, in := range inputs {
		outputs[i] = in.ID
	}
	return New(inputs, outputs)
}

// Validate checks that every input appears exactly once, every output id is
// unique and non-zero, and that time bases are explicit.
func (m Map) Validate() error {
	seenIn := make(map[uint32]struct{}, len(m.Entries))
	seenOut := make(map[uint32]struct{}, len(m.Entries))
	for _, e := range m.Entries {
		if e.InputID == 0 || e.OutputID == 0 || e.Timescale == 0 || !e.Kind.Valid() {
			return fmt.Errorf("%w: input=%d output=%d", ErrInvalidInput, e.InputID, e.OutputID)
		}
		if _, ok := seenIn[e.InputID]; ok {
			return fmt.Errorf("%w: input id %d", ErrDuplicateMapping, e.InputID)
		}
		if _, ok := seenOut[e.OutputID]; ok {
			return fmt.Errorf("%w: output id %d", ErrDuplicateMapping, e.OutputID)
		}
		seenIn[e.InputID] = struct{}{}
		seenOut[e.OutputID] = struct{}{}
	}
	return nil
}

// Lookup returns the entry for an input track id.
func (m Map) Lookup(inputID uint32) (Entry, bool) {
	for _, e := range m.Entries {
		if e.InputID == inputID {
			return e, true
		}
	}
	return Entry{}, false
}

// OutputIDs returns output ids in mapping order.
func (m Map) OutputIDs() []uint32 {
	out := make([]uint32, len(m.Entries))
	for i, e := range m.Entries {
		out[i] = e.OutputID
	}
	return out
}

// RequireComplete verifies that every input has a mapping and that the mapping
// has no extra entries.  It is intended to run immediately before remux.
func (m Map) RequireComplete(inputs []Input) error {
	if err := m.Validate(); err != nil {
		return err
	}
	seen := make(map[uint32]struct{}, len(inputs))
	for _, in := range inputs {
		seen[in.ID] = struct{}{}
		if _, ok := m.Lookup(in.ID); !ok {
			return fmt.Errorf("%w: input id %d", ErrMissingMapping, in.ID)
		}
	}
	for _, e := range m.Entries {
		if _, ok := seen[e.InputID]; !ok {
			return fmt.Errorf("%w: unknown input id %d", ErrMissingMapping, e.InputID)
		}
	}
	return nil
}
