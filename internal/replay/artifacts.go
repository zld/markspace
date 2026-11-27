package replay

import (
	"encoding/json"
	"os"
	"path/filepath"

	"replay/internal/clock"
	"replay/internal/graph"
	"replay/internal/ingest"
	"replay/internal/mapasset"
)

// Artefact file names, relative to the work directory.
const (
	fileIngest   = "ingest.json"
	fileTimeline = "timeline.json"
	fileGraph    = "graph.json"
	fileMap      = "map.json"
	filePlan     = "out/plan.json"
	fileManifest = "manifest.json"
	fileBaseline = "baseline.json"
)

// IngestArtifact is the persisted ingest result: stream counts, hashes
// and deficits. No payload bytes are stored.
type IngestArtifact struct {
	Streams          []ingest.Stream      `json:"streams"`
	Reports          []ingest.ClockReport `json:"reports"`
	PacketsProcessed int                  `json:"packets_processed"`
	ReportsByTrack   map[string]bool      `json:"reports_by_track"`
}

// TimelineArtifact is the persisted clock mapping.
type TimelineArtifact struct {
	Points    []clock.TimelinePoint `json:"points"`
	Sources   map[string]string     `json:"sources"`
	Estimated []string              `json:"estimated"`
	Wraps     int                   `json:"wraps"`
}

// GraphArtifact is the persisted dependency plan.
type GraphArtifact struct {
	Order  []string      `json:"order"`
	Tracks []graph.Track `json:"tracks"`
}

// AssetRecord is one generated MP4 file.
type AssetRecord struct {
	Version string `json:"version"`
	Track   string `json:"track"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
}

// TrackMap is one track's persisted mapping result.
type TrackMap struct {
	Track  string          `json:"track"`
	Result mapasset.Result `json:"result"`
	Assets []AssetRecord   `json:"assets"`
}

// MapArtifact is the persisted mapping for every track.
type MapArtifact struct {
	Tracks    []TrackMap `json:"tracks"`
	Missing   int        `json:"missing"`
	Conflicts int        `json:"conflicts"`
	Merged    int        `json:"merged"`
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".stage-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}
