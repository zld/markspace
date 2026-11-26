// Package recovery binds outputs to input digests and exact ranges in a
// checkpoint and resumes a run by verifying each artifact's SHA-256 first.
// An unchanged, hash-valid artifact is reused; a missing or corrupt one is
// rebuilt alone, so a single output hash failure never regenerates the other
// segments. Writes are atomic to keep a half-written file from masquerading
// as a product.
package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"replaykit/media/audit"
	"replaykit/media/fault"
)

// CheckpointVersion is the on-disk checkpoint schema.
const CheckpointVersion = 1

// ErrOutputVerification is returned when a written artifact still fails its
// hash check, which the CLI maps to exit status 1.
var ErrOutputVerification = errors.New("recovery: output verification failed")

// Item is one desired segment artifact. Bytes are the deterministic content
// to write when the artifact must be (re)built.
type Item struct {
	Key          string
	File         string
	InputDigest  string
	RangeStartNS int64
	RangeEndNS   int64
	MediaStart   int64
	MediaEnd     int64
	Hash         string // expected SHA-256 of Bytes
	Bytes        []byte
}

// Entry is one checkpoint row binding input digest and range to output hash.
type Entry struct {
	Key          string `json:"key"`
	File         string `json:"file"`
	InputDigest  string `json:"input_digest"`
	RangeStartNS int64  `json:"range_start_ns"`
	RangeEndNS   int64  `json:"range_end_ns"`
	MediaStart   int64  `json:"media_start_ticks"`
	MediaEnd     int64  `json:"media_end_ticks"`
	Bytes        int    `json:"bytes"`
	SHA256       string `json:"sha256"`
}

// Checkpoint is the content-addressed resume state.
type Checkpoint struct {
	Version  int               `json:"version"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Segments map[string]Entry  `json:"segments"`
}

// Outcome classifies one item after a resume pass.
type Outcome string

const (
	OutcomeReused  Outcome = "reused"
	OutcomeBuilt   Outcome = "built"
	OutcomeRebuilt Outcome = "rebuilt"
	OutcomeFailed  Outcome = "failed"
)

// ItemResult reports one resumed artifact.
type ItemResult struct {
	Key       string
	File      string
	Outcome   Outcome
	Hash      string
	HashMatch bool
	Anomalies []fault.Anomaly
}

// Result aggregates a resume pass.
type Result struct {
	Dir               string
	Results           []ItemResult
	Built             int
	Rebuilt           int
	Reused            int
	Failed            int
	HashMatched       int
	HashTotal         int
	DuplicateProducts int
	Anomalies         []fault.Anomaly
}

// MatchedAll reports whether every desired artifact verifies.
func (r *Result) MatchedAll() bool { return r.HashTotal > 0 && r.HashMatched == r.HashTotal }

func checkpointPath(dir string) string { return filepath.Join(dir, "checkpoint.json") }
func segmentDir(dir string) string     { return filepath.Join(dir, "segments") }
func segmentPath(dir, file string) string {
	return filepath.Join(segmentDir(dir), file)
}

// LoadCheckpoint reads a checkpoint; a missing file yields an empty one.
func LoadCheckpoint(dir string) (*Checkpoint, error) {
	b, err := os.ReadFile(checkpointPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return &Checkpoint{Version: CheckpointVersion, Segments: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := unmarshalCheckpoint(b, &cp); err != nil {
		// A corrupt checkpoint must not be trusted to skip work; start empty
		// so every artifact is re-verified against its own hash.
		return &Checkpoint{Version: CheckpointVersion, Segments: map[string]Entry{}}, nil
	}
	if cp.Segments == nil {
		cp.Segments = map[string]Entry{}
	}
	return &cp, nil
}

// Run verifies and resumes all items, writing checkpoint.json on success.
func Run(dir string, items []Item) (*Result, error) {
	if err := os.MkdirAll(segmentDir(dir), 0o755); err != nil {
		return nil, err
	}
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		return nil, err
	}
	res := &Result{Dir: dir, HashTotal: len(items)}
	expectedFiles := make(map[string]bool, len(items))

	for _, it := range items {
		expectedFiles[it.File] = true
		ir := ItemResult{Key: it.Key, File: it.File}
		path := segmentPath(dir, it.File)
		entry, hadEntry := cp.Segments[it.Key]
		identityMatch := hadEntry && entry.InputDigest == it.InputDigest &&
			entry.RangeStartNS == it.RangeStartNS && entry.RangeEndNS == it.RangeEndNS

		diskHash, hashErr := audit.HashFile(path)
		fileExists := hashErr == nil
		if hashErr != nil && !errors.Is(hashErr, os.ErrNotExist) {
			// Unreadable output is treated as a verification failure/rebuild.
			diskHash = ""
		}

		if identityMatch && fileExists && diskHash == it.Hash && diskHash == entry.SHA256 {
			ir.Outcome, ir.Hash, ir.HashMatch = OutcomeReused, diskHash, true
			res.Reused++
			res.HashMatched++
			res.Results = append(res.Results, ir)
			continue
		}

		// A prior product or checkpoint row that does not verify is a rebuild;
		// a brand-new key is a first build.
		wasPrior := hadEntry || fileExists
		if fileExists && diskHash != it.Hash {
			a := fault.New(layerOf(it.Key), 0, fault.CategoryHashMismatch, fault.SeverityError,
				fault.ActionRegenerated, 0, fault.Range{},
				fmt.Sprintf("output %s hash mismatch: on disk %s, expected %s", it.File, short(diskHash), short(it.Hash)))
			ir.Anomalies = append(ir.Anomalies, a)
			res.Anomalies = append(res.Anomalies, a)
		}

		if err := atomicWrite(path, it.Bytes); err != nil {
			ir.Outcome = OutcomeFailed
			ir.Anomalies = append(ir.Anomalies, fault.New(layerOf(it.Key), 0, fault.CategorySegmentFail,
				fault.SeverityFatal, fault.ActionNone, 0, fault.Range{}, err.Error()))
			res.Failed++
			res.Results = append(res.Results, ir)
			res.Anomalies = append(res.Anomalies, ir.Anomalies...)
			continue
		}
		got, err := audit.HashFile(path)
		if err != nil || got != it.Hash {
			ir.Outcome, ir.HashMatch = OutcomeFailed, false
			res.Failed++
			res.Anomalies = append(res.Anomalies, fault.New(layerOf(it.Key), 0, fault.CategoryHashMismatch,
				fault.SeverityFatal, fault.ActionNone, 0, fault.Range{},
				fmt.Sprintf("post-write verification failed for %s", it.File)))
			res.Results = append(res.Results, ir)
			continue
		}

		ir.Outcome, ir.Hash, ir.HashMatch = OutcomeBuilt, got, true
		if wasPrior {
			ir.Outcome = OutcomeRebuilt
			res.Rebuilt++
			res.Anomalies = append(res.Anomalies, fault.New(layerOf(it.Key), 0, fault.CategorySegmentRebuilt,
				fault.SeverityInfo, fault.ActionRegenerated, 0, fault.Range{},
				fmt.Sprintf("rebuilt affected segment %s only", it.File)))
		} else {
			res.Built++
		}
		res.HashMatched++
		cp.Segments[it.Key] = Entry{
			Key: it.Key, File: it.File, InputDigest: it.InputDigest,
			RangeStartNS: it.RangeStartNS, RangeEndNS: it.RangeEndNS,
			MediaStart: it.MediaStart, MediaEnd: it.MediaEnd,
			Bytes: len(it.Bytes), SHA256: got,
		}
		res.Results = append(res.Results, ir)
		res.Anomalies = append(res.Anomalies, ir.Anomalies...)
	}

	res.DuplicateProducts = countExtraFiles(segmentDir(dir), expectedFiles)

	if res.Failed == 0 {
		cp.Version = CheckpointVersion
		if err := writeCheckpoint(checkpointPath(dir), cp); err != nil {
			return res, err
		}
	} else {
		return res, fmt.Errorf("%w: %d segment(s) failed verification", ErrOutputVerification, res.Failed)
	}
	return res, nil
}

// Verify checks every item's on-disk hash without rebuilding. It returns the
// number matched and the list of keys that failed; a non-empty failure list
// is an output verification failure the CLI exits 1 for.
func Verify(dir string, items []Item) (matched int, bad []string) {
	for _, it := range items {
		got, err := audit.HashFile(segmentPath(dir, it.File))
		if err == nil && got == it.Hash {
			matched++
			continue
		}
		bad = append(bad, it.Key)
	}
	sort.Strings(bad)
	return matched, bad
}

// countExtraFiles counts regular files in the segment directory that are not
// canonical expected artifacts, which catches duplicate "(1)" style outputs.
func countExtraFiles(dir string, expected map[string]bool) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	extra := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		if !expected[e.Name()] {
			extra++
		}
	}
	return extra
}

func atomicWrite(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func layerOf(key string) string {
	if i := strings.Index(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return ""
}
