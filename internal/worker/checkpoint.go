// Package worker runs the pipeline in resumable stages. Each stage persists
// its output plus an input digest before the next one starts, so an interrupt
// resumes from the first incomplete stage and never redoes committed work.
package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rtpregress/internal/media"
)

// Stage names in execution order.
const (
	StageParse    = "parse"
	StageAlign    = "align"
	StageGenerate = "generate"
	StageVerify   = "verify"
)

// Stages lists the pipeline order. Resume replays only the suffix that has not
// been recorded as committed.
var Stages = []string{StageParse, StageAlign, StageGenerate, StageVerify}

// StageIndex returns the position of a stage name, or -1.
func StageIndex(name string) int {
	for i, s := range Stages {
		if s == name {
			return i
		}
	}
	return -1
}

// ManifestVersion is bumped when the manifest layout changes; a checkpoint
// from a different version is refused rather than misread.
const ManifestVersion = 1

// Manifest is the committed description of one run. It is written exactly
// once, by an atomic rename, and never mutated afterwards.
type Manifest struct {
	Version      int            `json:"version"`
	IdemKey      string         `json:"idemKey"`
	InputDigest  string         `json:"inputDigest"`
	ParamsDigest string         `json:"paramsDigest"`
	Stages       []StageRecord  `json:"stages"`
	Completed    bool           `json:"completed"`
	Artifact     ArtifactRecord `json:"artifact"`
	Summary      RunSummary     `json:"summary"`
}

// StageRecord is one stage's committed state and the digest of its input.
type StageRecord struct {
	Name         string `json:"name"`
	InputDigest  string `json:"inputDigest"`
	OutputDigest string `json:"outputDigest"`
	// Attempt is how many times the stage ran; duplicate delivery of the
	// same input leaves it at one.
	Attempt int `json:"attempt"`
}

// ArtifactRecord names the single output file and its content hash.
type ArtifactRecord struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// RunSummary is the compact result printed by the command.
type RunSummary struct {
	Sample         string         `json:"sample"`
	Output         string         `json:"output"`
	Events         int            `json:"events"`
	BySource       map[string]int `json:"bySource"`
	MaxDeviationMs float64        `json:"maxDeviationMs"`
	InputBytes     int            `json:"inputBytes"`
	OutputBytes    int            `json:"outputBytes"`
	Quality        float64        `json:"quality"`
	Segments       int            `json:"segments"`
	StageIndexes   map[string]int `json:"stageIndexes"`
	Resumed        bool           `json:"resumed"`
	RTPCommitted   int            `json:"rtpCommitted"`
	RTPLoss        int            `json:"rtpLoss"`
	RTPDuplicate   int            `json:"rtpDuplicates"`
	RTPLate        int            `json:"rtpLate"`
	LateStages     []string       `json:"lateStages"`
	Plan           map[string]any `json:"plan"`
}

// Digest hashes bytes deterministically and returns "sha256:<hex>".
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DigestOfFile hashes a file's contents.
func DigestOfFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Digest(data), nil
}

// ParamsDigest canonicalises the run parameters so two invocations with the
// same effective settings hash identically.
func ParamsDigest(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, params[k])
	}
	return Digest([]byte(b.String()))
}

// InputDigest combines the per-file digests in a fixed order.
func InputDigest(files map[string]string) string {
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s:%s\n", n, files[n])
	}
	return Digest([]byte(b.String()))
}

// ConflictError describes an idempotency-key reuse with different bytes.
func ConflictError(key string, haveInput, wantInput, haveParams, wantParams string) error {
	return media.Newf(media.KindConflict,
		"idempotency key %q already used with different %s (input %s vs %s, params %s vs %s)",
		key, differingField(haveInput, wantInput, haveParams, wantParams),
		haveInput, wantInput, haveParams, wantParams)
}

func differingField(haveIn, wantIn, havePar, wantPar string) string {
	switch {
	case haveIn != wantIn && havePar != wantPar:
		return "input and params"
	case haveIn != wantIn:
		return "input"
	default:
		return "params"
	}
}

// AtomicWrite writes data to path via a temporary sibling and a rename, so a
// reader never observes a partially written file.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return media.Newf(media.KindResume, "create temp for %s: %v", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return media.Newf(media.KindResume, "write %s: %v", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return media.Newf(media.KindResume, "sync %s: %v", path, err)
	}
	if err := tmp.Close(); err != nil {
		return media.Newf(media.KindResume, "close %s: %v", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return media.Newf(media.KindResume, "rename into %s: %v", path, err)
	}
	return nil
}

// LoadManifest reads and validates a manifest.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// Preserve os.ErrNotExist through the wrapper so callers can decide
		// between "first run" and "corrupt state".
		return nil, &media.Error{Kind: media.KindResume, Msg: "read manifest: " + err.Error(), Err: err}
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, media.Newf(media.KindResume, "manifest %s is corrupt: %v", path, err)
	}
	if m.Version != ManifestVersion {
		return nil, media.Newf(media.KindResume,
			"manifest version %d is not supported (want %d)", m.Version, ManifestVersion)
	}
	return &m, nil
}
