// Package manifest parses the camera manifest that describes each angle's
// source file, track identifiers, recording interval and editorial range.
// Parsing is strict: unknown fields, unsafe paths and inconsistent intervals
// are rejected with a manifest line number so a reviewer can find them.
package manifest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediad/internal/clock"
)

// Limits bound untrusted manifest input.
const (
	MaxManifestBytes = 1 << 20
	MaxEntries       = 4096
	MaxTracksPerCam  = 32
	MaxPathLen       = 512
)

// TrackSpec describes one track of a camera recording.
type TrackSpec struct {
	TrackID   uint32 `json:"track_id"`
	Kind      string `json:"kind"` // video or audio
	Timescale uint32 `json:"timescale"`
	// StartNs is the recording start on the shared timeline (session-relative).
	StartNs int64 `json:"start_ns"`
}

// Interval is a half-open nanosecond range [StartNs, EndNs).
type Interval struct {
	StartNs int64 `json:"start_ns"`
	EndNs   int64 `json:"end_ns"`
}

func (i Interval) Validate() error {
	if i.StartNs < 0 || i.EndNs <= i.StartNs {
		return fmt.Errorf("interval [%d,%d) is not positive", i.StartNs, i.EndNs)
	}
	return nil
}

// Entry is one camera angle in the manifest.
type Entry struct {
	Camera    string      `json:"camera"`
	File      string      `json:"file"`
	Tracks    []TrackSpec `json:"tracks"`
	Recording Interval    `json:"recording"`
	Editorial Interval    `json:"editorial"`
	Line      int         `json:"line"`
	SourceAbs string      `json:"-"` // resolved, validated absolute path
}

// Manifest is the parsed, validated camera manifest.
type Manifest struct {
	Version        int     `json:"version"`
	Seed           int64   `json:"seed,omitempty"`
	Corruption     string  `json:"corruption,omitempty"`
	SessionStartNs int64   `json:"session_start_ns"`
	TargetBps      int64   `json:"target_bps,omitempty"`
	Entries        []Entry `json:"entries"`
	Path           string  `json:"-"`
	SHA256         string  `json:"sha256,omitempty"`
}

// TrackBase builds the shared time base for a track, seeded with the
// recording start so nonzero source origins are preserved.
func (e Entry) TrackBase(spec TrackSpec) (clock.Base, error) {
	return clock.NewBase(spec.Timescale, e.Recording.StartNs+spec.StartNs)
}

// EditorialAlignment returns the manifest's expected editorial interval.
func (e Entry) EditorialAlignment() Interval { return e.Editorial }

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

// DecodeError describes a manifest problem with its 1-based line number.
type DecodeError struct {
	Line   int
	Code   clock.Code
	Detail string
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("manifest line %d: %s", e.Line, e.Detail)
}

func (e *DecodeError) Clock() *clock.Error {
	return clock.New(clock.StageParse, e.Code, "%s", e.Detail).WithLine(e.Line)
}

// ParseFile reads and validates a manifest from disk. Relative source paths
// resolve against the manifest directory; absolute paths are accepted only
// when they stay within root (default: manifest directory).
func ParseFile(path, root string) (*Manifest, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, clock.New(clock.StageParse, clock.CodeBadManifest, "stat manifest: %v", err)
	}
	if st.Size() > MaxManifestBytes {
		return nil, clock.New(clock.StageParse, clock.CodeOversizedTable, "manifest is %d bytes (limit %d)", st.Size(), MaxManifestBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.New(clock.StageParse, clock.CodeIO, "open manifest: %v", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	if err != nil {
		return nil, clock.New(clock.StageParse, clock.CodeIO, "read manifest: %v", err)
	}
	if len(raw) > MaxManifestBytes {
		return nil, clock.New(clock.StageParse, clock.CodeOversizedTable, "manifest exceeds %d bytes", MaxManifestBytes)
	}
	if root == "" {
		root = filepath.Dir(path)
	}
	m, err := Parse(bytes.NewReader(raw), root)
	if err != nil {
		return nil, err
	}
	m.Path = path
	return m, nil
}

// Parse decodes a JSON manifest. Source paths are resolved against root and
// must not escape it.
func Parse(r io.Reader, root string) (*Manifest, error) {
	dec := json.NewDecoder(io.LimitReader(r, MaxManifestBytes+1))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, clock.New(clock.StageParse, clock.CodeBadManifest, "json: %v", err)
	}
	if err := Validate(&m, root); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate enforces manifest invariants. root limits resolved source paths.
func Validate(m *Manifest, root string) error {
	if m.Version != 1 {
		return clock.New(clock.StageParse, clock.CodeBadManifest, "unsupported manifest version %d", m.Version)
	}
	if m.SessionStartNs < 0 {
		return clock.New(clock.StageParse, clock.CodeBadManifest, "negative session start")
	}
	if len(m.Entries) == 0 {
		return clock.New(clock.StageParse, clock.CodeBadManifest, "manifest has no entries")
	}
	if len(m.Entries) > MaxEntries {
		return clock.New(clock.StageParse, clock.CodeOversizedTable, "%d entries exceed limit %d", len(m.Entries), MaxEntries)
	}
	seenCam := map[string]int{}
	for i := range m.Entries {
		e := &m.Entries[i]
		line := i + 1
		if e.Camera == "" {
			derr := &DecodeError{Line: line, Code: clock.CodeBadManifest, Detail: "camera name is empty"}
			return derr.Clock()
		}
		if prev, dup := seenCam[e.Camera]; dup {
			return clock.New(clock.StageParse, clock.CodeBadManifest, "duplicate camera %q (first at entry %d)", e.Camera, prev).WithLine(line)
		}
		seenCam[e.Camera] = line
		if e.File == "" {
			return clock.New(clock.StageParse, clock.CodeBadManifest, "camera %q has no source file", e.Camera).WithLine(line)
		}
		if len(e.File) > MaxPathLen {
			return clock.New(clock.StageParse, clock.CodePathEscape, "source path too long").WithLine(line)
		}
		abs, err := resolveWithin(root, e.File)
		if err != nil {
			return clock.New(clock.StageParse, clock.CodePathEscape, "%v", err).WithLine(line).WithCamera(e.Camera)
		}
		e.SourceAbs = abs
		e.Line = line
		if err := e.Recording.Validate(); err != nil {
			return clock.New(clock.StageParse, clock.CodeBadManifest, "recording %v", err).WithLine(line).WithCamera(e.Camera)
		}
		if err := e.Editorial.Validate(); err != nil {
			return clock.New(clock.StageParse, clock.CodeBadManifest, "editorial %v", err).WithLine(line).WithCamera(e.Camera)
		}
		if e.Editorial.StartNs < e.Recording.StartNs || e.Editorial.EndNs > e.Recording.EndNs {
			return clock.New(clock.StageParse, clock.CodeBadManifest,
				"editorial [%d,%d) not inside recording [%d,%d)", e.Editorial.StartNs, e.Editorial.EndNs, e.Recording.StartNs, e.Recording.EndNs).
				WithLine(line).WithCamera(e.Camera)
		}
		if len(e.Tracks) == 0 {
			return clock.New(clock.StageParse, clock.CodeBadManifest, "camera %q has no tracks", e.Camera).WithLine(line)
		}
		if len(e.Tracks) > MaxTracksPerCam {
			return clock.New(clock.StageParse, clock.CodeOversizedTable, "camera %q has %d tracks (limit %d)", e.Camera, len(e.Tracks), MaxTracksPerCam).WithLine(line)
		}
		seenTrack := map[uint32]bool{}
		for _, t := range e.Tracks {
			if t.TrackID == 0 {
				return clock.New(clock.StageParse, clock.CodeBadManifest, "track id 0 is invalid").WithLine(line).WithCamera(e.Camera)
			}
			if seenTrack[t.TrackID] {
				return clock.New(clock.StageParse, clock.CodeBadManifest, "duplicate track id %d", t.TrackID).WithLine(line).WithCamera(e.Camera)
			}
			seenTrack[t.TrackID] = true
			switch t.Kind {
			case "video", "audio":
			default:
				return clock.New(clock.StageParse, clock.CodeBadManifest, "track %d has unknown kind %q", t.TrackID, t.Kind).WithLine(line).WithCamera(e.Camera)
			}
			if t.Timescale == 0 {
				return clock.New(clock.StageParse, clock.CodeBadManifest, "track %d has zero timescale", t.TrackID).WithLine(line).WithCamera(e.Camera)
			}
			if t.StartNs < 0 || t.StartNs >= e.Recording.EndNs-e.Recording.StartNs {
				return clock.New(clock.StageParse, clock.CodeBadManifest, "track %d start %d outside recording", t.TrackID, t.StartNs).WithLine(line).WithCamera(e.Camera)
			}
		}
	}
	return nil
}

// resolveWithin resolves a manifest path under root and rejects traversal.
func resolveWithin(root, rel string) (string, error) {
	if strings.ContainsRune(rel, '\x00') {
		return "", fmt.Errorf("path contains NUL")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute source path %q is not allowed", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, filepath.FromSlash(rel))
	cleaned := filepath.Clean(joined)
	relToRoot, err := filepath.Rel(absRoot, cleaned)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes manifest root", rel)
	}
	return cleaned, nil
}

// WriteExample emits a manifest used by the demo generator and tests.
func WriteExample(w io.Writer, m *Manifest) error {
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	enc := json.NewEncoder(bw)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// ClampNs clamps v into [lo,hi].
func ClampNs(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
