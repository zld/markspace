package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"continuity/internal/media"
)

const SchemaV2 = "continuity.manifest/v2"

// SegmentV2 is the versioned normalized segment record.
type SegmentV2 struct {
	ID         string `json:"id"`
	URI        string `json:"uri"`
	Track      string `json:"track"`
	Sequence   uint64 `json:"sequence"`
	StartNS    int64  `json:"start_ns"`
	DurationNS int64  `json:"duration_ns"`
}

// ManifestV2 is the current versioned manifest shape.
type ManifestV2 struct {
	Version       int         `json:"version"`
	Schema        string      `json:"schema"`
	Source        string      `json:"source"`
	Format        string      `json:"format"`
	MediaSequence uint64      `json:"media_sequence"`
	Segments      []SegmentV2 `json:"segments"`
}

type manifestV1 struct {
	Version       int         `json:"version"`
	Format        string      `json:"format"`
	Playlist      string      `json:"playlist"`
	MediaSequence uint64      `json:"media_sequence"`
	Items         []segmentV1 `json:"items"`
	Segments      []segmentV1 `json:"segments"`
}

type segmentV1 struct {
	ID         string `json:"id"`
	URI        string `json:"uri"`
	Track      string `json:"track"`
	Sequence   uint64 `json:"sequence"`
	StartNS    int64  `json:"start_ns"`
	DurationNS int64  `json:"duration_ns"`
	DurationMS int64  `json:"duration_ms"`
}

// LoadVersioned reads either v1 or v2 JSON and adapts it to V2.
func LoadVersioned(path string) (*ManifestV2, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeInvalidArgument, Source: path, Err: err}
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, raw, &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeInvalidManifest, Source: path, Err: err}
	}
	switch head.Version {
	case 2:
		var v2 ManifestV2
		if err := json.Unmarshal(raw, &v2); err != nil {
			return nil, raw, &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeInvalidManifest, Source: path, Err: err}
		}
		if err := ValidateV2(&v2); err != nil {
			return nil, raw, err
		}
		return &v2, raw, nil
	case 1, 0:
		var v1 manifestV1
		if err := json.Unmarshal(raw, &v1); err != nil {
			return nil, raw, &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeInvalidManifest, Source: path, Err: err}
		}
		v2 := adaptV1(&v1)
		if err := ValidateV2(v2); err != nil {
			return nil, raw, err
		}
		return v2, raw, nil
	default:
		return nil, raw, &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeInvalidManifest, Source: path,
			Detail: fmt.Sprintf("unsupported manifest version %d", head.Version)}
	}
}

func adaptV1(v1 *manifestV1) *ManifestV2 {
	items := v1.Items
	if len(items) == 0 {
		items = v1.Segments
	}
	format := v1.Format
	if format == "" {
		format = "hls"
	}
	out := &ManifestV2{Version: 2, Schema: SchemaV2, Format: format, Source: v1.Playlist,
		MediaSequence: v1.MediaSequence}
	for i, s := range items {
		dur := s.DurationNS
		if dur == 0 && s.DurationMS > 0 {
			dur = s.DurationMS * int64(media.Millisecond)
		}
		seq := s.Sequence
		if seq == 0 {
			seq = v1.MediaSequence + uint64(i)
		}
		id := s.ID
		if id == "" {
			id = fmt.Sprintf("seg-%d", seq)
		}
		out.Segments = append(out.Segments, SegmentV2{
			ID: id, URI: s.URI, Track: s.Track, Sequence: seq,
			StartNS: s.StartNS, DurationNS: dur,
		})
	}
	return out
}

// ValidateV2 checks the adapted manifest before it is promoted.
func ValidateV2(m *ManifestV2) error {
	if m == nil {
		return media.NewFault(media.PhaseMigrate, media.CodeInvalidManifest, "nil manifest")
	}
	if m.Version != 2 {
		return &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeInvalidManifest, Detail: "adapter did not produce v2"}
	}
	if len(m.Segments) == 0 {
		return &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeInvalidManifest, Detail: "manifest has no segments"}
	}
	seen := map[string]bool{}
	for _, s := range m.Segments {
		if s.URI == "" || s.DurationNS <= 0 {
			return &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeIllegalDuration, Detail: "segment missing URI or positive duration"}
		}
		key := s.Track + "|" + s.URI
		if seen[key] {
			return &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeDuplicateRef, Detail: "duplicate adapted reference " + key}
		}
		seen[key] = true
	}
	return nil
}

// CheckpointPath returns the pre-migration checkpoint location.
func CheckpointPath(path string) string { return path + ".checkpoint-v1" }

// ShadowPath returns the shadow-write location.
func ShadowPath(path string) string { return path + ".shadow" }

// CountCheckpoints counts preserved v1 checkpoints in a directory tree.
func CountCheckpoints(root string) (int, error) {
	count := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".checkpoint-v1") {
			count++
		}
		return nil
	})
	return count, err
}
