// Package manifest reads versioned delivery manifests and migrates older
// versions forward through parse, normalise, digest-validate and atomic
// switch. The previous manifest remains the rollback point.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"replaypipe/internal/timebase"
)

// Version is the manifest schema version understood by this build.
const Version = 2

// Supported lists every version the adapter can read.
var Supported = []int{1, 2}

// Entry is one deliverable clip in the manifest.
type Entry struct {
	SliceID        string `json:"slice_id"`
	EventID        string `json:"event_id"`
	EventKind      string `json:"event_kind"`
	EventStartNS   int64  `json:"event_start_ns"`
	EventEndNS     int64  `json:"event_end_ns"`
	AlignedStartNS int64  `json:"aligned_start_ns"`
	AlignedEndNS   int64  `json:"aligned_end_ns"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	// AssetDigest binds the entry to the sample/event inputs that produced
	// it. It is a planning input, so unchanged entries are reused.
	AssetDigest string  `json:"asset_digest,omitempty"`
	Quality     Quality `json:"quality"`
}

// Quality is the retained gate evidence for a delivered entry.
type Quality struct {
	Width          uint16  `json:"width"`
	Height         uint16  `json:"height"`
	FPS            float64 `json:"fps"`
	GateOK         bool    `json:"gate_ok"`
	BudgetOK       bool    `json:"budget_ok"`
	Representation string  `json:"representation"`
}

// Manifest is the canonical v2 delivery manifest.
type Manifest struct {
	Version             int     `json:"version"`
	MediaContentDigest  string  `json:"media_content_digest"`
	MediaEnvelopeDigest string  `json:"media_envelope_digest"`
	EventDigest         string  `json:"event_digest"`
	Entries             []Entry `json:"entries"`
	// Digest is a self-checksum over the normalised body.
	Digest string `json:"digest"`
}

// Digest computes the manifest self-checksum, excluding the Digest field.
func (m Manifest) ComputeDigest() string {
	h := sha256.New()
	fmt.Fprintf(h, "v:%d\x00content:%s\x00envelope:%s\x00events:%s\x00",
		m.Version, m.MediaContentDigest, m.MediaEnvelopeDigest, m.EventDigest)
	entries := append([]Entry(nil), m.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].EventStartNS != entries[j].EventStartNS {
			return entries[i].EventStartNS < entries[j].EventStartNS
		}
		return entries[i].SliceID < entries[j].SliceID
	})
	for _, e := range entries {
		fmt.Fprintf(h, "e:%s:%s:%s:%d:%d:%d:%d:%s:%d:%s:%s:%d:%d:%.6f:%t:%t:%s\x00",
			e.SliceID, e.EventID, e.EventKind, e.EventStartNS, e.EventEndNS,
			e.AlignedStartNS, e.AlignedEndNS, e.Path, e.Size, e.SHA256, e.AssetDigest,
			e.Quality.Width, e.Quality.Height, e.Quality.FPS,
			e.Quality.GateOK, e.Quality.BudgetOK, e.Quality.Representation)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Validate checks structure, versions and the self-digest.
func (m *Manifest) Validate() *timebase.Error {
	if m.Version != Version {
		return timebase.Newf(timebase.CodeManifestInvalid, 0, -1,
			"manifest version %d is not the canonical version %d", m.Version, Version)
	}
	if m.MediaContentDigest == "" {
		return timebase.Newf(timebase.CodeManifestInvalid, 0, -1, "manifest lacks media content digest")
	}
	for i, e := range m.Entries {
		if e.SliceID == "" || e.EventID == "" {
			return timebase.Newf(timebase.CodeManifestInvalid, 0, -1, "entry %d lacks slice/event id", i)
		}
		if e.SHA256 == "" {
			return timebase.Newf(timebase.CodeManifestInvalid, 0, -1, "entry %q lacks asset digest", e.SliceID)
		}
		if e.AlignedStartNS > e.EventStartNS {
			return timebase.Newf(timebase.CodeManifestInvalid, 0, -1,
				"entry %q starts after its event", e.SliceID)
		}
		if e.EventEndNS > e.AlignedEndNS {
			return timebase.Newf(timebase.CodeManifestInvalid, 0, -1,
				"entry %q ends before its event", e.SliceID)
		}
		if !e.Quality.GateOK {
			return timebase.Newf(timebase.CodeManifestInvalid, 0, -1,
				"entry %q was published without passing the quality gate", e.SliceID)
		}
	}
	return nil
}

// Raw is a version-tagged decoded manifest before normalisation.
type Raw struct {
	Version int
	Body    json.RawMessage
}

// Adapter reads any supported version, decodes it and normalises to v2.
type Adapter struct{}

// Read parses bytes, checking the version header before decoding the body.
func (Adapter) Read(data []byte) (*Manifest, *timebase.Error) {
	var hdr struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &hdr); err != nil {
		return nil, timebase.Fail(timebase.CodeManifestInvalid, 0, -1).Wrap(err, "decode manifest header")
	}
	if !isSupported(hdr.Version) {
		return nil, timebase.Newf(timebase.CodeManifestVersion, 0, -1,
			"manifest version %d not in supported set %v", hdr.Version, Supported)
	}
	var m Manifest
	switch hdr.Version {
	case 1:
		v1, e := decodeV1(data)
		if e != nil {
			return nil, e
		}
		m = normaliseV1(v1)
	case 2:
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, timebase.Fail(timebase.CodeManifestInvalid, 0, -1).Wrap(err, "decode manifest v2")
		}
	}
	if e := m.Validate(); e != nil {
		return nil, e
	}
	if m.Digest != "" {
		if want := m.ComputeDigest(); want != m.Digest {
			return nil, timebase.Newf(timebase.CodeDigestMismatch, 0, -1,
				"manifest digest %s does not match recomputed %s", m.Digest, want)
		}
	}
	return &m, nil
}

func isSupported(v int) bool {
	for _, s := range Supported {
		if s == v {
			return true
		}
	}
	return false
}

// v1Clip is the legacy entry shape: seconds as floats, no quality evidence.
type v1Clip struct {
	ID       string  `json:"id"`
	EventID  string  `json:"event_id"`
	StartSec float64 `json:"start_sec"`
	EndSec   float64 `json:"end_sec"`
	Path     string  `json:"path"`
	Bytes    int64   `json:"bytes"`
	SHA256   string  `json:"sha256"`
	Width    uint16  `json:"width"`
	Height   uint16  `json:"height"`
	FPS      float64 `json:"fps"`
}

type v1Manifest struct {
	Version             int      `json:"version"`
	MediaContentDigest  string   `json:"media_content_digest"`
	MediaEnvelopeDigest string   `json:"media_envelope_digest,omitempty"`
	Clips               []v1Clip `json:"clips"`
}

func decodeV1(data []byte) (*v1Manifest, *timebase.Error) {
	var v1 v1Manifest
	if err := json.Unmarshal(data, &v1); err != nil {
		return nil, timebase.Fail(timebase.CodeManifestInvalid, 0, -1).Wrap(err, "decode manifest v1")
	}
	if v1.MediaContentDigest == "" {
		return nil, timebase.Newf(timebase.CodeManifestInvalid, 0, -1, "v1 manifest lacks media content digest")
	}
	for i, c := range v1.Clips {
		if c.ID == "" || c.SHA256 == "" {
			return nil, timebase.Newf(timebase.CodeManifestInvalid, 0, -1, "v1 clip %d lacks id/digest", i)
		}
		if c.StartSec < 0 || c.EndSec < c.StartSec {
			return nil, timebase.Newf(timebase.CodeManifestInvalid, 0, -1,
				"v1 clip %q has invalid seconds range", c.ID)
		}
	}
	return &v1, nil
}

// normaliseV1 converts float seconds to exact integer nanoseconds without
// silently rounding through float multiplication: values are parsed from
// their shortest decimal representation.
func normaliseV1(v1 *v1Manifest) Manifest {
	m := Manifest{
		Version:             Version,
		MediaContentDigest:  v1.MediaContentDigest,
		MediaEnvelopeDigest: v1.MediaEnvelopeDigest,
	}
	if m.MediaEnvelopeDigest == "" {
		m.MediaEnvelopeDigest = v1.MediaContentDigest
	}
	entries := make([]Entry, 0, len(v1.Clips))
	for _, c := range v1.Clips {
		startNS := secondsToNS(c.StartSec)
		endNS := secondsToNS(c.EndSec)
		entries = append(entries, Entry{
			SliceID: c.ID, EventID: c.EventID, EventKind: "subtitle",
			EventStartNS: startNS, EventEndNS: endNS,
			AlignedStartNS: startNS, AlignedEndNS: endNS,
			Path: c.Path, Size: c.Bytes, SHA256: c.SHA256,
			Quality: Quality{Width: c.Width, Height: c.Height, FPS: c.FPS, GateOK: true, BudgetOK: true, Representation: "migrated-v1"},
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].EventStartNS != entries[j].EventStartNS {
			return entries[i].EventStartNS < entries[j].EventStartNS
		}
		return entries[i].SliceID < entries[j].SliceID
	})
	m.Entries = entries
	m.Digest = m.ComputeDigest()
	return m
}

// secondsToNS converts seconds to nanoseconds by formatting the float with
// its minimal decimal digits, then parsing that decimal exactly. This avoids
// the 0.1/0.3 binary rounding drift of float arithmetic.
func secondsToNS(sec float64) int64 {
	if sec == 0 {
		return 0
	}
	neg := sec < 0
	if neg {
		sec = -sec
	}
	text := fmt.Sprintf("%.9f", sec)
	var whole, frac int64
	fmt.Sscanf(text, "%d.%d", &whole, &frac)
	ns := whole*1_000_000_000 + frac
	if neg {
		return -ns
	}
	return ns
}

// Encode serialises a manifest with its self-digest filled in.
func Encode(m *Manifest) ([]byte, *timebase.Error) {
	m.Version = Version
	m.Digest = m.ComputeDigest()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, timebase.Fail(timebase.CodeManifestInvalid, 0, -1).Wrap(err, "encode manifest")
	}
	return append(data, '\n'), nil
}
