package manifest

import (
	"path/filepath"
	"strconv"
	"strings"

	"continuity/internal/core"
	"continuity/internal/timebase"
)

// ParseHLS parses a local Media Playlist (m3u8). Blank lines and tags are
// tolerated; every segment URI keeps its 1-based line number.
func ParseHLS(path string) (*Playlist, error) {
	data, err := readLimited(path, MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	dir := dirOf(path)
	pl := &Playlist{Path: path, Dir: dir}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "#EXTM3U" {
		return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, 1, -1,
			"missing #EXTM3U", nil)
	}
	var seq int64
	var durTicks int64
	for i, raw := range lines[1:] {
		lineNo := i + 2
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "#EXT-X-MEDIA-SEQUENCE:") {
			v := strings.TrimPrefix(s, "#EXT-X-MEDIA-SEQUENCE:")
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, lineNo, -1,
					"bad media sequence", err)
			}
			seq = n
			continue
		}
		if strings.HasPrefix(s, "#EXTINF:") {
			v := strings.TrimPrefix(s, "#EXTINF:")
			v = strings.TrimSuffix(v, ",")
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, lineNo, -1,
					"bad EXTINF", err)
			}
			durTicks = int64(f*float64(timebase.TicksPerSecond) + 0.5)
			continue
		}
		if strings.HasPrefix(s, "#") {
			continue // other tags ignored
		}
		rel, err := resolveURI(dir, s, path, lineNo)
		if err != nil {
			return nil, err
		}
		pl.Segments = append(pl.Segments, Segment{URI: rel, Line: lineNo, Index: seq, DurTicks: durTicks})
		seq++
		durTicks = 0
	}
	if len(pl.Segments) == 0 {
		return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, 0, -1,
			"no segments", nil)
	}
	return pl, nil
}

func dirOf(path string) string {
	d := filepath.Dir(path)
	if d == "" {
		return "."
	}
	return d
}
