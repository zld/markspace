package mp4

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
)

// SourceKind enumerates supported local metadata source types.
type SourceKind string

const (
	SourceMP4 SourceKind = "mp4"
	SourceRTP SourceKind = "rtp"
)

// Source is one manifest entry. Paths are relative to the manifest directory.
type Source struct {
	ID          string     `json:"id"`
	Kind        SourceKind `json:"kind"`
	Path        string     `json:"path"`
	TrackNumber int        `json:"track_number"`
	TimeScale   uint32     `json:"timescale,omitempty"`
	Role        string     `json:"role,omitempty"`
}

// Manifest is the local inventory consumed by the parser subsystem.
type Manifest struct {
	Name           string   `json:"name"`
	MovieTimeScale uint32   `json:"movie_timescale,omitempty"`
	Sources        []Source `json:"sources"`
	Dir            string   `json:"-"`
	Path           string   `json:"-"`
}

// LoadManifest reads JSON and resolves source paths against its directory.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, 0, 0, "read manifest", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, clock.Wrap(clock.CategoryInvalidArgument, path, 0, 0, "decode manifest JSON", err)
	}
	if len(m.Sources) == 0 {
		return nil, clock.New(clock.CategoryInvalidArgument, path, 0, 0, "manifest has no sources")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, 0, 0, "absolute manifest path", err)
	}
	m.Path = abs
	m.Dir = filepath.Dir(abs)
	for i := range m.Sources {
		if m.Sources[i].TrackNumber == 0 {
			m.Sources[i].TrackNumber = i + 1
		}
		if !filepath.IsAbs(m.Sources[i].Path) {
			m.Sources[i].Path = filepath.Join(m.Dir, m.Sources[i].Path)
		}
	}
	return &m, nil
}

// ParsedSource pairs manifest identity with either an MP4 or RTP parse result.
type ParsedSource struct {
	Source Source
	MP4    *Track
	File   *File
	RTP    *RTPTrack
}

// ParseManifest parses every listed source using the unified error model.
func ParseManifest(m *Manifest) ([]ParsedSource, error) {
	out := make([]ParsedSource, 0, len(m.Sources))
	for _, src := range m.Sources {
		ps := ParsedSource{Source: src}
		switch src.Kind {
		case SourceMP4:
			file, err := ParseFile(src.Path)
			if err != nil {
				return nil, err
			}
			file.Path = src.Path
			if src.TrackNumber < 1 || src.TrackNumber > len(file.Tracks) {
				return nil, clock.New(clock.CategoryMissingTrack, src.Path, src.TrackNumber, 0,
					"manifest track number not found in MP4")
			}
			tr := file.Tracks[src.TrackNumber-1]
			tr.Number = src.TrackNumber
			tr.Path = src.Path
			if src.TimeScale != 0 && clock.TicksPerSecond(src.TimeScale) != tr.TimeScale {
				return nil, clock.New(clock.CategoryTimebase, src.Path, src.TrackNumber, tr.TimebaseOffset,
					"manifest timescale is incompatible with MP4 mdhd timescale")
			}
			ps.File = file
			ps.MP4 = tr
		case SourceRTP:
			scale := clock.TicksPerSecond(src.TimeScale)
			if scale == 0 {
				scale = 90000
			}
			rtpTrack, err := ParseRTP(src.Path, src.TrackNumber, scale)
			if err != nil {
				return nil, err
			}
			ps.RTP = rtpTrack
		default:
			return nil, clock.New(clock.CategoryInvalidArgument, m.Path, src.TrackNumber, 0,
				"unsupported source kind "+string(src.Kind))
		}
		out = append(out, ps)
	}
	return out, nil
}
