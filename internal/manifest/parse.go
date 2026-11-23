// Package manifest translates the JSON asset manifest supplied by edge
// nodes into validated track declarations, and later emits the forwarding
// manifest.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
)

// Bounds enforced on untrusted edge-node input.
const (
	// MaxManifestBytes caps the manifest text itself.
	MaxManifestBytes = 4 << 20
	// MaxInputBytes caps each referenced MP4 file.
	MaxInputBytes = 512 << 20
	// MaxSamples caps the total sample count accepted from one asset.
	MaxSamples = 8_000_000
	// MaxDurationMS caps presentation duration.
	MaxDurationMS = 6 * 3600 * 1000
)

// Supported codecs.  Only linear PCM is decoded; anything else is explicitly
// rejected rather than silently passed through.
const (
	CodecPCM16 = "pcm_s16le"
)

// Roles recognised in track declarations.
const (
	RoleAudio = "audio"
	RoleVideo = "video"
)

// EditDecl mirrors one elst entry.  MediaTime is in the track media timescale,
// Duration is in the movie timescale.  An empty edit has MediaTime -1.
type EditDecl struct {
	MediaTime int64 `json:"media_time"`
	Duration  int64 `json:"duration"`
}

// TrackDecl is one declared track in the asset.
type TrackDecl struct {
	ID              uint32     `json:"id"`
	Role            string     `json:"role"`
	Codec           string     `json:"codec"`
	Required        bool       `json:"required"`
	DependsOn       []uint32   `json:"depends_on"`
	Path            string     `json:"path"`
	SampleRate      int        `json:"sample_rate"`
	Channels        int        `json:"channels"`
	Layout          string     `json:"layout"`
	MediaTimescale  uint32     `json:"media_timescale"`
	Edits           []EditDecl `json:"edits"`
	SparseKeyframes bool       `json:"sparse_keyframes"`
}

// Asset is the parsed and validated manifest.
type Asset struct {
	Version        int         `json:"version"`
	AssetID        string      `json:"asset_id"`
	MovieTimescale uint32      `json:"movie_timescale"`
	StartTick      int64       `json:"start_tick"`
	Tracks         []TrackDecl `json:"tracks"`

	// Dir is the directory used to resolve relative track paths.
	Dir string `json:"-"`
}

// MovieRate returns the presentation rate as a rational clock rate.
func (a *Asset) MovieRate() (clock.Rate, error) {
	if a.MovieTimescale == 0 {
		return clock.Rate{}, fault.Fatal(fault.ClassManifest, "movie-timescale", "movie_timescale is zero")
	}
	return clock.NewRate(int64(a.MovieTimescale), 1)
}

// TrackByID returns the declaration with the given id.
func (a *Asset) TrackByID(id uint32) (TrackDecl, bool) {
	for _, t := range a.Tracks {
		if t.ID == id {
			return t, true
		}
	}
	return TrackDecl{}, false
}

// ResolvePath returns the absolute path for a declared track.
func (a *Asset) ResolvePath(t TrackDecl) string {
	if filepath.IsAbs(t.Path) {
		return t.Path
	}
	return filepath.Join(a.Dir, t.Path)
}

// AudioTrack returns the single required audio track, if declared.
func (a *Asset) AudioTrack() (TrackDecl, bool) {
	for _, t := range a.Tracks {
		if t.Role == RoleAudio {
			return t, true
		}
	}
	return TrackDecl{}, false
}

// ParseFile reads a manifest from disk with the size cap applied.
func ParseFile(path string) (*Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Fatal(fault.ClassInput, "manifest-open", err.Error()).Because(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fault.Fatal(fault.ClassInput, "manifest-stat", err.Error()).Because(err)
	}
	if st.Size() > MaxManifestBytes {
		return nil, fault.Fatal(fault.ClassLimit, "manifest-size", fmt.Sprintf("manifest is %d bytes, cap is %d", st.Size(), MaxManifestBytes))
	}
	a, err := Parse(f)
	if err != nil {
		return nil, err
	}
	a.Dir = filepath.Dir(path)
	return a, nil
}

// Parse decodes and validates a manifest.  Unknown fields are rejected so a
// malformed node cannot smuggle behaviour past validation.
func Parse(r io.Reader) (*Asset, error) {
	dec := json.NewDecoder(io.LimitReader(r, MaxManifestBytes))
	dec.DisallowUnknownFields()
	var a Asset
	if err := dec.Decode(&a); err != nil {
		return nil, fault.Fatal(fault.ClassManifest, "parse", err.Error()).Because(err)
	}
	// Reject trailing garbage after the top-level object.
	if err := dec.Decode(new(struct{})); err != io.EOF {
		return nil, fault.Fatal(fault.ClassManifest, "trailing-data", "manifest contains trailing data")
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return &a, nil
}

// Validate checks structural rules that do not need the track graph.
func (a *Asset) Validate() error {
	if a.Version != 1 {
		return fault.Fatal(fault.ClassManifest, "version", fmt.Sprintf("unsupported manifest version %d", a.Version))
	}
	if strings.TrimSpace(a.AssetID) == "" {
		return fault.Fatal(fault.ClassManifest, "asset-id", "asset_id is empty")
	}
	if a.MovieTimescale == 0 {
		return fault.Fatal(fault.ClassManifest, "movie-timescale", "movie_timescale is zero")
	}
	if a.StartTick < 0 {
		return fault.Fatal(fault.ClassManifest, "start-tick", "start_tick is negative")
	}
	if len(a.Tracks) == 0 {
		return fault.Fatal(fault.ClassDependency, "no-tracks", "manifest declares no tracks")
	}
	seen := make(map[uint32]int, len(a.Tracks))
	audioCount := 0
	for i, t := range a.Tracks {
		where := fmt.Sprintf("tracks[%d]", i)
		if t.ID == 0 {
			return fault.Fatal(fault.ClassManifest, "track-id", where+": id must be non-zero")
		}
		if _, dup := seen[t.ID]; dup {
			return fault.Fatal(fault.ClassManifest, "duplicate-track", fmt.Sprintf("%s: duplicate track id %d", where, t.ID))
		}
		seen[t.ID] = i
		switch t.Role {
		case RoleAudio:
			audioCount++
		case RoleVideo:
		default:
			return fault.Fatal(fault.ClassManifest, "role", fmt.Sprintf("%s: unknown role %q", where, t.Role))
		}
		if len(t.DependsOn) > 0 {
			edges := make(map[uint32]bool, len(t.DependsOn))
			for _, dep := range t.DependsOn {
				if dep == t.ID {
					return fault.Fatal(fault.ClassDependency, "self-edge", fmt.Sprintf("%s: track depends on itself", where))
				}
				if edges[dep] {
					return fault.Fatal(fault.ClassDependency, "duplicate-edge", fmt.Sprintf("%s: duplicate dependency on %d", where, dep))
				}
				edges[dep] = true
			}
		}
		if t.Path == "" {
			return fault.Fatal(fault.ClassManifest, "path", where+": path is empty")
		}
		if err := validateTrackDecl(where, t); err != nil {
			return err
		}
	}
	if audioCount > 1 {
		return fault.Fatal(fault.ClassManifest, "audio-tracks", "more than one audio track is declared")
	}
	return nil
}

func validateTrackDecl(where string, t TrackDecl) error {
	// Unsupported codecs are refused outright when required; optional tracks
	// are reported by the graph as skipped rather than failing here.
	if t.Codec == "" {
		return fault.Fatal(fault.ClassManifest, "codec", where+": codec is empty")
	}
	if t.Role != RoleAudio {
		return nil
	}
	if t.Codec != CodecPCM16 {
		f := fault.Fatal(fault.ClassManifest, "codec-unsupported", fmt.Sprintf("%s: codec %q is not supported for audio", where, t.Codec))
		if !t.Required {
			f = f.AsRecoverable().AtByte(0)
		}
		return f
	}
	if t.SampleRate != 44100 && t.SampleRate != 48000 {
		return fault.Fatal(fault.ClassAudio, "sample-rate", fmt.Sprintf("%s: sample rate %d is not 44100 or 48000", where, t.SampleRate))
	}
	if t.MediaTimescale != uint32(t.SampleRate) {
		return fault.Fatal(fault.ClassTimeline, "media-timescale", fmt.Sprintf("%s: media_timescale %d must equal sample_rate %d", where, t.MediaTimescale, t.SampleRate))
	}
	switch t.Layout {
	case "mono", "stereo", "5.1":
	default:
		return fault.Fatal(fault.ClassAudio, "layout-unknown", fmt.Sprintf("%s: unknown layout %q", where, t.Layout))
	}
	wantCh := map[string]int{"mono": 1, "stereo": 2, "5.1": 6}[t.Layout]
	if t.Channels != wantCh {
		return fault.Fatal(fault.ClassAudio, "layout-channels", fmt.Sprintf("%s: layout %q needs %d channels, declaration has %d", where, t.Layout, wantCh, t.Channels))
	}
	for i, e := range t.Edits {
		if e.Duration <= 0 {
			return fault.Fatal(fault.ClassTimeline, "edit-duration", fmt.Sprintf("%s: edits[%d] has non-positive duration", where, i))
		}
		if e.MediaTime < -1 {
			return fault.Fatal(fault.ClassTimeline, "edit-media", fmt.Sprintf("%s: edits[%d] has invalid media_time %d", where, i, e.MediaTime))
		}
	}
	return nil
}

// SortedTrackIDs returns track ids in ascending order for deterministic
// iteration and output.
func (a *Asset) SortedTrackIDs() []uint32 {
	ids := make([]uint32, len(a.Tracks))
	for i, t := range a.Tracks {
		ids[i] = t.ID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Canonical renders the manifest back to stable JSON (two-space indent, no
// HTML escaping) for hashing and debugging.
func (a *Asset) Canonical() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	type plain Asset
	p := plain(*a)
	p.Dir = ""
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
