// Package index builds and queries disk-backed random-access media indexes.
package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"assetpack/internal/adapter"
	"assetpack/internal/clock"
)

const (
	IndexVersion      = 1
	IndexFileName     = "index.json"
	MaxPageCacheBytes = 4 << 20
)

// Record is the persisted metadata for one sample. Payload bytes stay in source.
type Record struct {
	Index    int         `json:"index"`
	PTS      clock.Ticks `json:"pts"`
	Duration clock.Ticks `json:"duration"`
	Offset   int64       `json:"offset"`
	Size     int         `json:"size"`
	Key      bool        `json:"key"`
	Preroll  []int       `json:"preroll,omitempty"`
	Seq      uint16      `json:"rtp_seq,omitempty"`
}

// Track is an indexed elementary stream.
type Track struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Codec      adapter.ID     `json:"codec"`
	Timebase   clock.Rational `json:"timebase"`
	Duration   clock.Ticks    `json:"duration"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
	SampleRate int            `json:"sample_rate,omitempty"`
	Channels   int            `json:"channels,omitempty"`
	Records    []Record       `json:"records"`
	byIndex    map[int]int
}

// Event is a persisted timed event.
type Event struct {
	ID         int         `json:"id"`
	TrackID    string      `json:"track_id"`
	PTS        clock.Ticks `json:"pts"`
	Duration   clock.Ticks `json:"duration"`
	Scheme     string      `json:"scheme_id_uri"`
	Value      string      `json:"value"`
	MessageSHA string      `json:"message_sha256,omitempty"`
}

// Index is the JSON disk index. It never contains float timestamps or payloads.
type Index struct {
	Version    int         `json:"version"`
	SourceName string      `json:"source_name"`
	SourcePath string      `json:"source_path"`
	SourceSHA  string      `json:"source_sha256"`
	Duration   clock.Ticks `json:"duration"`
	Tracks     []*Track    `json:"tracks"`
	Events     []Event     `json:"events,omitempty"`
	trackByID  map[string]*Track
	path       string
}

// Build converts a parsed asset into a persistable index.
func Build(asset *adapter.Asset, sourceRelPath string) (*Index, error) {
	if asset == nil {
		return nil, fmt.Errorf("cannot index nil asset")
	}
	idx := &Index{
		Version: IndexVersion, SourceName: asset.SourceName, SourcePath: sourceRelPath,
		SourceSHA: asset.SourceSHA, Duration: asset.Duration, trackByID: map[string]*Track{},
	}
	for _, at := range asset.Tracks {
		tr := &Track{
			ID: at.ID, Kind: at.Kind, Codec: at.Codec, Timebase: at.Timebase,
			Duration: at.Duration, Width: at.Width, Height: at.Height,
			SampleRate: at.SampleRate, Channels: at.Channels, byIndex: map[int]int{},
		}
		var lastEnd clock.Ticks
		for i, s := range at.Samples {
			if s.Index != i {
				return nil, clock.Wrap(at.ID, "index-order", s.Offset,
					fmt.Errorf("sample position %d has index %d", i, s.Index))
			}
			end := s.PTS + s.Duration
			if i > 0 && s.PTS < lastEnd {
				return nil, clock.Wrap(at.ID, "index-monotonic", s.Offset,
					fmt.Errorf("sample %d PTS %d precedes prior end %d", i, s.PTS, lastEnd))
			}
			for _, pre := range s.Preroll {
				if pre < 0 || pre >= len(at.Samples) {
					return nil, clock.Wrap(at.ID, "preroll", s.Offset, fmt.Errorf("sample %d has invalid preroll %d", i, pre))
				}
			}
			tr.Records = append(tr.Records, Record{
				Index: i, PTS: s.PTS, Duration: s.Duration, Offset: s.Offset, Size: s.Size,
				Key: s.Key, Preroll: append([]int(nil), s.Preroll...), Seq: s.Seq,
			})
			tr.byIndex[i] = i
			lastEnd = end
		}
		idx.Tracks = append(idx.Tracks, tr)
		idx.trackByID[tr.ID] = tr
	}
	for _, e := range asset.Events {
		idx.Events = append(idx.Events, Event{
			ID: e.ID, TrackID: e.TrackID, PTS: e.PTS, Duration: e.Duration,
			Scheme: e.SchemeIDURI, Value: e.Value,
		})
	}
	return idx, nil
}

// Save atomically writes index.json into dir.
func (idx *Index) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return "", err
	}
	final := filepath.Join(dir, IndexFileName)
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", err
	}
	idx.path = final
	return final, nil
}

// Load reads and validates a disk index.
func Load(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("decode index %s: %w", path, err)
	}
	if idx.Version != IndexVersion {
		return nil, fmt.Errorf("%w: index version %d", adapter.ErrUnknownVersion, idx.Version)
	}
	idx.path = path
	idx.trackByID = map[string]*Track{}
	for _, tr := range idx.Tracks {
		tr.byIndex = map[int]int{}
		for i, r := range tr.Records {
			tr.byIndex[r.Index] = i
		}
		idx.trackByID[tr.ID] = tr
	}
	return &idx, nil
}

func (idx *Index) Track(id string) (*Track, error) {
	tr, ok := idx.trackByID[id]
	if !ok {
		return nil, fmt.Errorf("index has no track %q", id)
	}
	return tr, nil
}

// ResolveSource returns the source path relative to the index directory.
func (idx *Index) ResolveSource(indexDir string) string {
	if filepath.IsAbs(idx.SourcePath) {
		return idx.SourcePath
	}
	return filepath.Join(indexDir, idx.SourcePath)
}
