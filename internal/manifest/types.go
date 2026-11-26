// Package manifest parses DASH (SegmentTemplate/SegmentTimeline) and HLS
// media playlists from local files. Every diagnostic carries file plus line
// or byte-offset location.
package manifest

import (
	"os"
	"path/filepath"
	"strings"

	"continuity/internal/clock"
)

// MaxManifestBytes bounds a single manifest parse (defensive; manifests are
// small local files). Segment files are separately bounded at 64 MiB.
const MaxManifestBytes = 8 << 20

// MaxSegmentBytes is the hard ceiling for one media segment (64 MiB).
const MaxSegmentBytes int64 = 64 << 20

// Kind identifies a manifest family.
type Kind string

const (
	KindDASH Kind = "dash"
	KindHLS  Kind = "hls"
)

// Segment is one media chunk reference on a representation/variant timeline.
type Segment struct {
	Index    int       // 0-based position within its representation
	Sequence int       // media sequence number (HLS) or number (DASH)
	URL      string    // sanitized path relative to the manifest directory
	Start    clock.Rat // presentation start, seconds, relative to manifest origin
	Duration clock.Rat // exact duration in seconds
	ByteSize int64     // resolved file size in bytes (0 until media inspection)
	Line     int       // 1-based source line (HLS) / XML element line (DASH)
	Offset   int64     // byte offset within source when line is unavailable (-1)
}

// Representation is one DASH representation or one HLS variant playlist.
type Representation struct {
	ID            string
	BandwidthBPS  int64 // declared target bitrate (DASH @bandwidth / HLS BANDWIDTH)
	Segments      []Segment
	// Discontinuities holds the Segment.Index values at which a discontinuity
	// boundary begins (HLS EXT-X-DISCONTINUITY); empty for plain DASH.
	Discontinuities []int
}

// Manifest is the parsed, family-neutral view of an input manifest.
type Manifest struct {
	Kind            Kind
	Path            string // absolute manifest path used for resolution
	Dir             string // directory containing the manifest
	MediaPresentSec clock.Rat
	Representations []Representation
}

// SafePath resolves a manifest-relative reference against root, rejecting
// URLs, absolute paths and any traversal that escapes root. The returned path
// is always cleaned and rooted inside root.
func SafePath(root, ref string) (string, error) {
	if ref == "" {
		return "", clock.New(clock.KindInvalidInput, "safe-path", "empty path reference")
	}
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") {
		return "", clock.New(clock.KindInvalidInput, "safe-path",
			"URL references are not permitted: %q", ref)
	}
	if filepath.IsAbs(ref) {
		return "", clock.New(clock.KindInvalidInput, "safe-path",
			"absolute paths are not permitted: %q", ref)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", clock.New(clock.KindIO, "safe-path", "resolve root: %v", err)
	}
	joined := filepath.Join(cleanRoot, filepath.FromSlash(ref))
	rel, err := filepath.Rel(cleanRoot, joined)
	if err != nil {
		return "", clock.New(clock.KindInvalidInput, "safe-path",
			"cannot relativize %q: %v", ref, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", clock.New(clock.KindInvalidInput, "safe-path",
			"path escapes input root: %q", ref)
	}
	return joined, nil
}

// CheckSegmentFiles verifies every referenced segment exists as a regular
// file and records its byte size; used by both parsers after resolution.
func CheckSegmentFiles(m *Manifest) error {
	for ri := range m.Representations {
		rep := &m.Representations[ri]
		for si := range rep.Segments {
			seg := &rep.Segments[si]
			full, err := SafePath(m.Dir, seg.URL)
			if err != nil {
				return clock.AtFile(clock.AtLine(err, seg.Line), m.Path)
			}
			info, err := os.Stat(full)
			if err != nil {
				return clock.AtFile(clock.AtLine(
					clock.New(clock.KindInvalidInput, "manifest",
						"segment file missing %q: %v", seg.URL, err), seg.Line), m.Path)
			}
			if info.IsDir() {
				return clock.AtFile(clock.AtLine(
					clock.New(clock.KindInvalidInput, "manifest",
						"segment reference is a directory: %q", seg.URL), seg.Line), m.Path)
			}
			seg.ByteSize = info.Size()
		}
	}
	return nil
}

// DetectKind classifies a manifest path by extension.
func DetectKind(path string) (Kind, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mpd":
		return KindDASH, nil
	case ".m3u8":
		return KindHLS, nil
	default:
		return "", clock.New(clock.KindInvalidInput, "manifest",
			"unknown manifest extension for %q", path)
	}
}
