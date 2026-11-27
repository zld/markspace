// Package manifest writes and verifies the run manifest. The manifest is
// the artefact the acceptance checks compare: it hashes every published
// file together with its logical identity, so identical inputs must yield
// an identical manifest hash and any change must change it.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"replay/internal/fault"
)

// SchemaVersion is bumped when the manifest document shape changes.
const SchemaVersion = 1

// Entry is one published artefact.
type Entry struct {
	// Path is relative to the work directory.
	Path string `json:"path"`
	// Kind names the artefact role: "plan", "asset", "checkpoint".
	Kind string `json:"kind"`
	// SHA256 is the lowercase hex digest of the file bytes.
	SHA256 string `json:"sha256"`
	// Size is the file length in bytes.
	Size int64 `json:"size"`
}

// Source records provenance for a published artefact: which version and
// track it came from, and the input digest that produced it.
type Source struct {
	Path        string   `json:"path"`
	Track       string   `json:"track"`
	Version     string   `json:"version"`
	IntervalNS  [2]int64 `json:"interval_ns"`
	InputDigest string   `json:"input_digest"`
}

// StageHash records a completed stage's input and output hashes so a
// resume can detect changed inputs.
type StageHash struct {
	Stage      string `json:"stage"`
	InputHash  string `json:"input_hash"`
	OutputHash string `json:"output_hash"`
}

// Document is the manifest file.
type Document struct {
	SchemaVersion int         `json:"schema_version"`
	RunID         string      `json:"run_id"`
	Case          string      `json:"case"`
	Versions      []string    `json:"versions"`
	Entries       []Entry     `json:"entries"`
	Sources       []Source    `json:"sources"`
	Stages        []StageHash `json:"stages"`
	// ArtifactHash is the aggregate digest over every entry, computed
	// with this field empty. Acceptance compares it.
	ArtifactHash string `json:"artifact_hash"`
	// Stats is a compact summary printed to the terminal.
	Stats Stats `json:"stats"`
}

// Stats is the acceptance-relevant summary.
type Stats struct {
	Tracks         int `json:"tracks"`
	Packets        int `json:"packets"`
	Lost           int `json:"lost"`
	Missing        int `json:"missing"`
	Conflicts      int `json:"conflicts"`
	Wraps          int `json:"wraps"`
	DuplicateCount int `json:"duplicates"`
}

// Build assembles a manifest from published entries and sources, then
// computes the aggregate hash. Entries are sorted by path so input order
// never affects the result.
func Build(runID, runCase string, versions []string, entries []Entry, sources []Source, stages []StageHash, stats Stats) (*Document, error) {
	sortedEntries := append([]Entry(nil), entries...)
	sort.SliceStable(sortedEntries, func(i, j int) bool { return sortedEntries[i].Path < sortedEntries[j].Path })
	for _, e := range sortedEntries {
		if e.Path == "" || e.SHA256 == "" {
			return nil, fault.New(fault.KindManifest, "manifest entry missing path or digest").At("manifest")
		}
		if len(e.SHA256) != 64 {
			return nil, fault.New(fault.KindManifest, "manifest entry digest is not SHA-256").At("manifest")
		}
	}
	sortedSources := append([]Source(nil), sources...)
	sort.SliceStable(sortedSources, func(i, j int) bool {
		if sortedSources[i].Track != sortedSources[j].Track {
			return sortedSources[i].Track < sortedSources[j].Track
		}
		if sortedSources[i].Path != sortedSources[j].Path {
			return sortedSources[i].Path < sortedSources[j].Path
		}
		return sortedSources[i].IntervalNS[0] < sortedSources[j].IntervalNS[0]
	})
	sortedStages := append([]StageHash(nil), stages...)
	sort.SliceStable(sortedStages, func(i, j int) bool { return sortedStages[i].Stage < sortedStages[j].Stage })

	doc := &Document{
		SchemaVersion: SchemaVersion,
		RunID:         runID,
		Case:          runCase,
		Versions:      append([]string(nil), versions...),
		Entries:       sortedEntries,
		Sources:       sortedSources,
		Stages:        sortedStages,
		Stats:         stats,
	}
	hash, err := AggregateHash(doc)
	if err != nil {
		return nil, err
	}
	doc.ArtifactHash = hash
	return doc, nil
}

// AggregateHash computes the digest of the manifest's stable content:
// schema, run identity, versions, every entry, source, stage hash and
// stats. Volatile metadata (the digest itself) is excluded. The run ID
// is included so two structurally identical runs are still comparable
// only when they share a run ID; the CLI derives the ID from inputs.
func AggregateHash(doc *Document) (string, error) {
	if doc == nil {
		return "", fault.New(fault.KindManifest, "cannot hash nil manifest").At("manifest")
	}
	canonical := struct {
		SchemaVersion int         `json:"schema_version"`
		RunID         string      `json:"run_id"`
		Case          string      `json:"case"`
		Versions      []string    `json:"versions"`
		Entries       []Entry     `json:"entries"`
		Sources       []Source    `json:"sources"`
		Stages        []StageHash `json:"stages"`
		Stats         Stats       `json:"stats"`
	}{
		SchemaVersion: doc.SchemaVersion,
		RunID:         doc.RunID,
		Case:          doc.Case,
		Versions:      doc.Versions,
		Entries:       doc.Entries,
		Sources:       doc.Sources,
		Stages:        doc.Stages,
		Stats:         doc.Stats,
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fault.New(fault.KindManifest, "cannot encode manifest").At("manifest").Wrap(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Write publishes the manifest to dir/manifest.json atomically and
// returns its path and artifact hash.
func (d *Document) Write(dir string) (path, digest string, err error) {
	if dir == "" {
		return "", "", fault.New(fault.KindArgument, "manifest directory required").At("manifest")
	}
	if err := d.verifySelf(); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fault.New(fault.KindInput, "cannot create manifest directory").At("manifest").Wrap(err)
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", "", fault.New(fault.KindManifest, "cannot encode manifest").At("manifest").Wrap(err)
	}
	data = append(data, '\n')
	target := filepath.Join(dir, "manifest.json")
	if err := d.writeAtomic(target, data); err != nil {
		return "", "", err
	}
	return target, d.ArtifactHash, nil
}

func (d *Document) verifySelf() error {
	if d.SchemaVersion != SchemaVersion {
		return fault.New(fault.KindManifest, "manifest schema version mismatch").At("manifest")
	}
	hash, err := AggregateHash(d)
	if err != nil {
		return err
	}
	if d.ArtifactHash != "" && d.ArtifactHash != hash {
		return fault.New(fault.KindManifest, "manifest content does not match its artifact hash").At("manifest")
	}
	d.ArtifactHash = hash
	return nil
}

func (d *Document) writeAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return fault.New(fault.KindInput, "cannot create temp manifest").At("manifest").Wrap(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fault.New(fault.KindInput, "cannot write manifest").At("manifest").Wrap(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fault.New(fault.KindInput, "cannot sync manifest").At("manifest").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return fault.New(fault.KindInput, "cannot close manifest").At("manifest").Wrap(err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fault.New(fault.KindInput, "cannot publish manifest").At("manifest").Wrap(err)
	}
	return nil
}

// Diff describes how two manifests differ.
type Diff struct {
	Same         bool     `json:"same"`
	HashChanged  bool     `json:"hash_changed"`
	LeftHash     string   `json:"left_hash"`
	RightHash    string   `json:"right_hash"`
	AddedPaths   []string `json:"added,omitempty"`
	RemovedPaths []string `json:"removed,omitempty"`
	ChangedPaths []string `json:"changed,omitempty"`
	total        int
}

// Changes returns the number of differing artefacts plus one when the
// aggregate hash differs for a reason not attributable to an entry.
func (d Diff) Changes() int {
	n := len(d.AddedPaths) + len(d.RemovedPaths) + len(d.ChangedPaths)
	if n == 0 && d.HashChanged {
		n = 1
	}
	return n
}

// Compare diffs a current manifest against a baseline. Hash equality is
// the acceptance signal; the path lists explain any mismatch.
func Compare(baseline, current *Document) Diff {
	diff := Diff{}
	if baseline == nil || current == nil {
		diff.HashChanged = true
		if current != nil {
			diff.RightHash = current.ArtifactHash
		}
		if baseline != nil {
			diff.LeftHash = baseline.ArtifactHash
		}
		return diff
	}
	diff.LeftHash = baseline.ArtifactHash
	diff.RightHash = current.ArtifactHash
	diff.HashChanged = baseline.ArtifactHash != current.ArtifactHash

	left := make(map[string]Entry, len(baseline.Entries))
	for _, e := range baseline.Entries {
		left[e.Path] = e
	}
	right := make(map[string]Entry, len(current.Entries))
	for _, e := range current.Entries {
		right[e.Path] = e
	}
	for path, e := range right {
		if prev, ok := left[path]; !ok {
			diff.AddedPaths = append(diff.AddedPaths, path)
		} else if prev.SHA256 != e.SHA256 {
			diff.ChangedPaths = append(diff.ChangedPaths, path)
		}
	}
	for path := range left {
		if _, ok := right[path]; !ok {
			diff.RemovedPaths = append(diff.RemovedPaths, path)
		}
	}
	sort.Strings(diff.AddedPaths)
	sort.Strings(diff.RemovedPaths)
	sort.Strings(diff.ChangedPaths)
	diff.Same = !diff.HashChanged && diff.Changes() == 0
	return diff
}

// Load reads a manifest from path and checks its internal digest.
func Load(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New(fault.KindInput, "cannot read manifest").At("manifest").Wrap(err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fault.New(fault.KindManifest, "manifest is not valid JSON").At("manifest").Wrap(err)
	}
	if doc.SchemaVersion != SchemaVersion {
		return nil, fault.New(fault.KindManifest, "unsupported manifest schema version").At("manifest")
	}
	hash, err := AggregateHash(&doc)
	if err != nil {
		return nil, err
	}
	if doc.ArtifactHash != hash {
		return nil, fault.New(fault.KindManifest, "manifest hash does not match content").At("manifest")
	}
	return &doc, nil
}

// Verify checks every entry against the files under dir and confirms the
// aggregate hash. It returns the paths that failed so the caller can
// report them all at once.
func Verify(dir string, doc *Document) ([]string, error) {
	if doc == nil {
		return nil, fault.New(fault.KindManifest, "no manifest to verify").At("manifest")
	}
	hash, err := AggregateHash(doc)
	if err != nil {
		return nil, err
	}
	if doc.ArtifactHash != hash {
		return nil, fault.New(fault.KindManifest, "manifest hash does not match content").At("manifest")
	}
	var bad []string
	for _, entry := range doc.Entries {
		full := filepath.Join(dir, entry.Path)
		if !within(dir, full) {
			return nil, fault.New(fault.KindManifest, "manifest path escapes workspace").
				At("manifest").WithTrack(entry.Path)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			bad = append(bad, entry.Path+" (missing)")
			continue
		}
		if int64(len(data)) != entry.Size {
			bad = append(bad, entry.Path+" (size)")
			continue
		}
		if HashFile(data) != entry.SHA256 {
			bad = append(bad, entry.Path+" (digest)")
		}
	}
	if len(bad) > 0 {
		return bad, fault.New(fault.KindManifest, "manifest verification failed: "+strings.Join(bad, ", ")).
			At("manifest")
	}
	return nil, nil
}

// HashFile returns the lowercase hex SHA-256 of data.
func HashFile(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashPath hashes a single file on disk.
func HashPath(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fault.New(fault.KindInput, "cannot read artefact ").
			At("manifest").WithTrack(filepath.Base(path)).Wrap(err)
	}
	return HashFile(data), int64(len(data)), nil
}

// EntryFor builds an entry for a file beneath dir, storing a relative
// path so the manifest is portable across workspaces.
func EntryFor(dir, path, kind string) (Entry, error) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return Entry{}, fault.New(fault.KindInput, "artefact is outside workspace").At("manifest").Wrap(err)
	}
	if strings.HasPrefix(rel, "..") {
		return Entry{}, fault.New(fault.KindInput, "artefact is outside workspace").At("manifest")
	}
	digest, size, err := HashPath(path)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: filepath.ToSlash(rel), Kind: kind, SHA256: digest, Size: size}, nil
}

func within(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Describe renders a one-line manifest summary.
func (d *Document) Describe() string {
	return fmt.Sprintf("manifest hash=%s entries=%d stages=%d conflicts=%d missing=%d",
		ShortHash(d.ArtifactHash), len(d.Entries), len(d.Stages), d.Stats.Conflicts, d.Stats.Missing)
}

// ShortHash abbreviates a hex digest for the terminal.
func ShortHash(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:16]
}
