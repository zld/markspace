// Package model holds the canonical in-memory representation of delivered
// tracks and their metadata. Subsystems exchange these types rather than
// raw manifest maps, so validation rules live in one place.
package model

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/pion/rtp"
)

// Kind discriminates video frame sequences from audio sample tracks.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// Hash is a SHA-256 digest that serializes as a lowercase hex string.
type Hash [32]byte

// ParseHash decodes a 64-character lowercase hex digest.
func ParseHash(s string) (Hash, error) {
	var h Hash
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return h, fmt.Errorf("model: invalid sha256 %q", s)
	}
	copy(h[:], raw)
	return h, nil
}

// String renders the digest as lowercase hex.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// MarshalJSON / UnmarshalJSON keep hashes readable in manifests and summaries.
func (h Hash) MarshalJSON() ([]byte, error) { return json.Marshal(h.String()) }

func (h *Hash) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseHash(s)
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// ColorMeta describes a frame's color tagging. Source records where the
// label came from (manifest entry, container metadata, ...) so provenance is
// preserved after normalization.
type ColorMeta struct {
	Label     string `json:"label"`
	Source    string `json:"source"`
	Primaries string `json:"primaries,omitempty"`
	Transfer  string `json:"transfer,omitempty"`
	Matrix    string `json:"matrix,omitempty"`
}

// Frame is one delivered video frame or audio chunk descriptor. The RTP
// header reuses pion/rtp's SequenceNumber (uint16) and Timestamp (uint32)
// types so delivery ordering uses the same wire semantics as RTP sources.
type Frame struct {
	TrackID   string      `json:"track_id"`
	Index     int         `json:"index"`
	Name      string      `json:"name"`
	Length    int64       `json:"length"`
	SHA256    Hash        `json:"sha256"`
	TimeUnits int64       `json:"time_units"`
	TimeRate  int64       `json:"time_rate"`
	Color     ColorMeta   `json:"color"`
	RTP       rtp.Header  `json:"rtp"`
}

// Track is a single video or audio delivery with its full metadata block.
type Track struct {
	ID       string `json:"id"`
	Kind     Kind   `json:"kind"`
	Frames   []Frame `json:"frames"`

	// Video.
	FPS int64 `json:"fps,omitempty"`

	// Audio.
	SampleRate       int64   `json:"sample_rate,omitempty"`
	Channels         int     `json:"channels,omitempty"`
	Layout           string  `json:"layout,omitempty"`
	StartOffsetUnits int64   `json:"start_offset_units,omitempty"`
	StartOffsetRate  int64   `json:"start_offset_rate,omitempty"`
	LoudnessLUFS     float64 `json:"loudness_lufs,omitempty"`
}

// Timeline is the complete delivery set handed to the engine.
type Timeline struct {
	Tracks []*Track `json:"tracks"`
}

// TrackByID returns the track with the given id, or nil if absent.
func (tl *Timeline) TrackByID(id string) *Track {
	for _, tr := range tl.Tracks {
		if tr.ID == id {
			return tr
		}
	}
	return nil
}

// Validate enforces the structural rules shared by every subsystem:
// unique ids, known kinds, contiguous frame indices and positive time bases.
func (tl *Timeline) Validate() error {
	seen := map[string]bool{}
	for _, tr := range tl.Tracks {
		if tr.ID == "" {
			return fmt.Errorf("model: track with empty id")
		}
		if seen[tr.ID] {
			return fmt.Errorf("model: duplicate track id %q", tr.ID)
		}
		seen[tr.ID] = true
		switch tr.Kind {
		case KindVideo:
			if tr.FPS <= 0 {
				return fmt.Errorf("model: video track %q has invalid fps %d", tr.ID, tr.FPS)
			}
		case KindAudio:
			if tr.SampleRate <= 0 || tr.Channels <= 0 {
				return fmt.Errorf("model: audio track %q has invalid rate/channels", tr.ID)
			}
		default:
			return fmt.Errorf("model: track %q has unknown kind %q", tr.ID, tr.Kind)
		}
		for i, f := range tr.Frames {
			if f.TrackID != tr.ID {
				return fmt.Errorf("model: frame track id %q != owning track %q", f.TrackID, tr.ID)
			}
			if f.Index != i {
				return fmt.Errorf("model: track %q frame indices not contiguous: got %d at %d", tr.ID, f.Index, i)
			}
			if f.TimeRate <= 0 {
				return fmt.Errorf("model: track %q frame %d has non-positive time rate", tr.ID, i)
			}
		}
	}
	return nil
}
