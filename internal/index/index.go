// Package index persists scan results with content-addressed input summaries
// so unchanged inputs are never rescanned and any change invalidates the
// entry. Files are written to a temp name, checksummed and atomically renamed.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediapack/internal/model"
	"mediapack/internal/probe"
)

// IndexVersion is the on-disk schema version; bumps force a full rescan.
const IndexVersion = "index-2"

// InputSummary is the cheap, deterministic fingerprint of one input. Any
// field changing invalidates the cached scan.
type InputSummary struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	MTime  int64  `json:"mtime_unix_nano"`
	Mode   uint32 `json:"mode"`
}

// TrackSummary is a compact fingerprint of a scanned track, used to detect
// changes in declared metadata even when the file digest comparison is bypassed.
type TrackSummary struct {
	ID        uint32 `json:"id"`
	Kind      string `json:"kind"`
	TimeScale uint32 `json:"time_scale"`
	Samples   int64  `json:"samples"`
	Duration  int64  `json:"duration_ts"`
	Hash      string `json:"hash"`
}

// Entry is one persisted index record.
type Entry struct {
	IndexVersion string          `json:"index_version"`
	ScanVersion  string          `json:"scan_version"`
	Summary      InputSummary    `json:"input_summary"`
	Tracks       []TrackSummary  `json:"track_summaries"`
	Meta         *probe.FileMeta `json:"meta"`
}

// Store is a directory-backed index.
type Store struct {
	dir string
}

// NewStore opens (or lazily creates) an index directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) pathFor(sum InputSummary) string {
	key := fmt.Sprintf("%s|%d|%s", filepath.Base(sum.Path), sum.Size, sum.SHA256)
	d := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, "idx-"+hex.EncodeToString(d[:16])+".json")
}

// Summarize builds an InputSummary for a file without retaining its bytes.
func Summarize(path string) (InputSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return InputSummary{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return InputSummary{}, err
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := copyBuffer(h, f, buf); err != nil {
		return InputSummary{}, err
	}
	return InputSummary{
		Path: path, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil)),
		MTime: st.ModTime().UnixNano(), Mode: uint32(st.Mode()),
	}, nil
}

// Lookup returns a cached entry only when every summary still matches and the
// index/scan schema versions are current. A stale or corrupt entry yields
// (nil, nil) so callers transparently rescan.
func (s *Store) Lookup(want InputSummary) (*Entry, error) {
	raw, err := os.ReadFile(s.pathFor(want))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil || e.Meta == nil {
		// Corrupt index: remove rather than fail the whole pipeline.
		_ = os.Remove(s.pathFor(want))
		return nil, nil
	}
	if e.IndexVersion != IndexVersion || e.ScanVersion != probe.ScanVersion {
		return nil, nil
	}
	if e.Summary != want {
		return nil, nil
	}
	return &e, nil
}

// Save persists an entry built from a fresh scan, atomically.
func (s *Store) Save(sum InputSummary, meta *probe.FileMeta) (*Entry, error) {
	e := &Entry{
		IndexVersion: IndexVersion,
		ScanVersion:  probe.ScanVersion,
		Summary:      sum,
		Meta:         meta,
		Tracks:       summarizeTracks(meta),
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, err
	}
	final := s.pathFor(sum)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return nil, err
	}
	if err := verifyJSON(tmp); err != nil {
		_ = os.Remove(tmp)
		return nil, &model.PipelineError{
			Stage: model.StageIndex, Code: model.CodeCorruptIndex,
			Input: sum.Path, ByteRange: model.ByteRange{Offset: 0, Length: int64(len(raw))},
			Message: "index failed round-trip verification: " + err.Error(),
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return e, nil
}

// Invalidate deletes any entry matching the summary (used on input change).
func (s *Store) Invalidate(sum InputSummary) error {
	err := os.Remove(s.pathFor(sum))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func summarizeTracks(meta *probe.FileMeta) []TrackSummary {
	out := make([]TrackSummary, 0, len(meta.Tracks))
	for _, t := range meta.Tracks {
		h := sha256.New()
		fmt.Fprintf(h, "%d|%s|%d|%d|%d|%v|%v",
			t.ID, t.Handler, t.TimeScale, t.SampleCount, t.DurationTS,
			t.Rotation != nil, t.CleanAperture != nil)
		out = append(out, TrackSummary{
			ID: t.ID, Kind: string(t.Kind), TimeScale: t.TimeScale,
			Samples: t.SampleCount, Duration: t.DurationTS,
			Hash: hex.EncodeToString(h.Sum(nil)),
		})
	}
	return out
}

func verifyJSON(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var e Entry
	return json.Unmarshal(raw, &e)
}
