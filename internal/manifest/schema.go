// Package manifest defines the versioned delivery manifest and the adapter that
// maps old and new field names onto one canonical form. An unknown version or a
// field that has no mapping stops migration with the offending field path; no
// field is ever silently dropped.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

// Supported schema versions. The manifest is JSON with an explicit version
// field; each version may name fields differently, which the adapter resolves.
const (
	VersionLegacy = 1 // tracks[].rate_hz, tracks[].layout, colour{matrix,transfer,primaries}
	VersionV2     = 2 // audio{sample_rate_hz,channel_layout}, video.colour{...}
)

// SupportedVersions lists every version this build can read, newest first.
var SupportedVersions = []int{VersionV2, VersionLegacy}

// Manifest is the canonical, version-independent delivery contract.
type Manifest struct {
	Version       int
	AssetID       string
	Title         string
	Languages     []string
	AudioRates    map[uint64]RateRange
	Layouts       map[uint64]ChannelLayout
	VideoColour   map[uint64]ColourRule
	MaxChannels   uint64
	RequireColour bool
	// SequenceBase is the expected first transport sequence, or -1.
	SequenceBase int64
	// Tracks holds the declared track ordinals in document order.
	Tracks []TrackDecl
	// FieldMap records the source path for every canonical field so output can
	// be audited back to the exact bytes of the input manifest.
	FieldMap map[string]string
	// Raw retains the original document for digesting.
	Raw []byte
}

// TrackDecl is one declared track.
type TrackDecl struct {
	Number  uint64
	Type    string
	CodecID string
	Lang    string
	Name    string
}

// Alias the validator's rule types so the manifest layer cannot drift from the
// rules it feeds.
type (
	RateRange     = tracks.RateRange
	ChannelLayout = tracks.ChannelLayout
	ColourRule    = tracks.ColourRule
)

// ProfileFor converts the manifest into the track-validator profile.
type Profile struct {
	AudioRates    map[uint64]RateRange
	Layouts       map[uint64]ChannelLayout
	VideoColour   map[uint64]ColourRule
	MaxChannels   uint64
	RequireColour bool
}

// Profile returns the validation profile implied by the manifest.
func (m *Manifest) Profile() Profile {
	return Profile{
		AudioRates:    m.AudioRates,
		Layouts:       m.Layouts,
		VideoColour:   m.VideoColour,
		MaxChannels:   m.MaxChannels,
		RequireColour: m.RequireColour,
	}
}

// Load reads and adapts a manifest from disk.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, timeline.Wrap(err, timeline.StageManifest, timeline.CatIO, 0, -1,
			"read manifest %s", path)
	}
	return Parse(data)
}

// Parse adapts raw manifest JSON into the canonical form.
func Parse(data []byte) (*Manifest, error) {
	var probe struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, timeline.Wrap(err, timeline.StageManifest, timeline.CatManifestField, 0, -1,
			"manifest is not valid JSON")
	}
	if len(probe.Version) == 0 {
		return nil, timeline.New(timeline.StageManifest, timeline.CatManifestField, 0, -1,
			"manifest has no version field")
	}
	var version int
	if err := json.Unmarshal(probe.Version, &version); err != nil {
		return nil, timeline.New(timeline.StageManifest, timeline.CatManifestVersion, 0, -1,
			"manifest version is not an integer: %s", string(probe.Version))
	}
	if !supported(version) {
		return nil, timeline.New(timeline.StageManifest, timeline.CatManifestVersion, 0, -1,
			"manifest version %d is not supported (supported: %v)", version, SupportedVersions)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]interface{}
	if err := dec.Decode(&doc); err != nil {
		return nil, timeline.Wrap(err, timeline.StageManifest, timeline.CatManifestField, 0, -1,
			"decode manifest object")
	}
	if err := ensureEOF(dec); err != nil {
		return nil, err
	}
	m := &Manifest{
		Version:      version,
		AudioRates:   map[uint64]RateRange{},
		Layouts:      map[uint64]ChannelLayout{},
		VideoColour:  map[uint64]ColourRule{},
		SequenceBase: -1,
		FieldMap:     map[string]string{},
		Raw:          append([]byte(nil), data...),
	}
	adapter, ok := adapters[version]
	if !ok {
		return nil, timeline.New(timeline.StageManifest, timeline.CatManifestVersion, 0, -1,
			"no adapter registered for manifest version %d", version)
	}
	if err := adapter(m, doc); err != nil {
		return nil, err
	}
	return m, nil
}

func ensureEOF(dec *json.Decoder) error {
	var extra interface{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return timeline.New(timeline.StageManifest, timeline.CatManifestField, 0, -1,
				"manifest has trailing content after the top-level object")
		}
		return timeline.Wrap(err, timeline.StageManifest, timeline.CatManifestField, 0, -1,
			"manifest trailing content")
	}
	return nil
}

func supported(v int) bool {
	for _, s := range SupportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// fieldErr builds a manifest field error naming the JSON path.
func fieldErr(path, format string, args ...interface{}) *timeline.Error {
	e := timeline.New(timeline.StageManifest, timeline.CatManifestField, 0, -1, format, args...)
	return e.WithField(path)
}

// unknownFieldError is used when a version-specific field has no canonical
// mapping. It must stop migration, never drop the field.
func unknownFieldError(path string, version int) *timeline.Error {
	e := timeline.New(timeline.StageManifest, timeline.CatManifestField, 0, -1,
		"field has no mapping for manifest version %d", version)
	return e.WithField(path)
}

// knownTopLevel lists canonical top-level keys any adapter may consume.
var knownTopLevel = map[string]bool{
	"version": true, "asset_id": true, "title": true, "languages": true,
	"audio": true, "video": true, "tracks": true, "max_channels": true,
	"require_colour": true, "sequence_base": true,
}

// checkKeys rejects unknown top-level keys with their path. Adapters that map a
// legacy name add it to their own allowance before calling this.
func checkKeys(doc map[string]interface{}, allowed map[string]bool, version int) error {
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if allowed[k] {
			continue
		}
		if knownTopLevel[k] {
			continue
		}
		return unknownFieldError(k, version)
	}
	return nil
}

func num(v interface{}, path string) (float64, error) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, fieldErr(path, "value is not a number: %v", err)
		}
		return f, nil
	case float64:
		return n, nil
	default:
		return 0, fieldErr(path, "value is not a number")
	}
}

func uintVal(v interface{}, path string) (uint64, error) {
	f, err := num(v, path)
	if err != nil {
		return 0, err
	}
	if f < 0 || f != float64(uint64(f)) {
		return 0, fieldErr(path, "value %v is not a non-negative integer", f)
	}
	return uint64(f), nil
}

func str(v interface{}, path string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fieldErr(path, "value is not a string")
	}
	return s, nil
}

func trackNumber(v interface{}, path string) (uint64, error) {
	n, err := uintVal(v, path)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, fieldErr(path, "track number must be positive")
	}
	return n, nil
}

// mapTrackMap walks a {"<track>": {...}} object, passing each entry to fn with
// the JSON path of that entry.
func mapTrackMap(v interface{}, base string, fn func(track uint64, path string, body map[string]interface{}) error) error {
	switch body := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			track, err := parseTrackKey(k, base+"."+k)
			if err != nil {
				return err
			}
			entry, ok := body[k].(map[string]interface{})
			if !ok {
				return fieldErr(base+"."+k, "track entry is not an object")
			}
			if err := fn(track, base+"."+k, entry); err != nil {
				return err
			}
		}
		return nil
	default:
		return fieldErr(base, "expected an object keyed by track number")
	}
}

func parseTrackKey(k, path string) (uint64, error) {
	var n uint64
	if _, err := fmt.Sscanf(k, "%d", &n); err != nil || n == 0 {
		return 0, fieldErr(path, "track key %q is not a positive integer", k)
	}
	return n, nil
}
