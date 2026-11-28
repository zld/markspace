package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"archive-lab/internal/manifest"
	"archive-lab/internal/media"
)

// Kind tags which role a file plays in the index.
type Kind string

const (
	KindManifest Kind = "manifest"
	KindMedia    Kind = "segment"
	KindMetadata Kind = "metadata"
	KindInit     Kind = "init"
)

// FileEntry is one hashed input file.
type FileEntry struct {
	Kind   Kind   `json:"kind"`
	Path   string `json:"path"` // relative to the input root, slash separated
	URI    string `json:"uri"`  // as it appears in the manifest
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Index is the committed result of a scan. It is written atomically; a
// reader never observes a partially built index.
type Index struct {
	Version    int         `json:"version"`
	Manifest   FileEntry   `json:"manifest"`
	Files      []FileEntry `json:"files"`
	TotalBytes int64       `json:"total_bytes"`
	Digest     string      `json:"digest"`
}

// Limits bounds one scan.
type Limits struct {
	MaxSegmentBytes int64
	MaxTotalBytes   int64
	MaxFiles        int
	// ValidateContainers walks every media segment with the MP4 box parser so
	// a damaged container fails the scan instead of committing an index.
	ValidateContainers bool
}

// DefaultLimits are generous but finite; they bound memory and IO.
var DefaultLimits = Limits{
	MaxSegmentBytes: 64 << 20,
	MaxTotalBytes:   4 << 30,
	MaxFiles:        10_000,
}

func (l Limits) resolved() Limits {
	if l.MaxSegmentBytes <= 0 {
		l.MaxSegmentBytes = DefaultLimits.MaxSegmentBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = DefaultLimits.MaxTotalBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultLimits.MaxFiles
	}
	return l
}

// SegmentRef relates a parsed segment to its resolved relative path.
type SegmentRef struct {
	Index int
	URI   string
	Rel   string
}

// Resolve validates that ref stays strictly inside root and returns the
// slash-separated relative path. Absolute paths, URLs and ".." escapes fail.
func Resolve(root, ref string) (string, error) {
	if ref == "" {
		return "", media.New(media.StageScan, "resolve", "empty path").WithPath(root)
	}
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") {
		return "", media.New(media.StageScan, "resolve", "remote reference %q is not allowed", ref).WithPath(root)
	}
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") {
		return "", media.New(media.StageScan, "resolve", "absolute path %q is not allowed", ref).WithPath(root)
	}
	clean := filepath.Clean(filepath.FromSlash(ref))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", media.New(media.StageScan, "resolve", "path %q escapes the input root", ref).WithPath(root)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", media.Wrap(media.StageScan, "resolve", err, "invalid root").WithPath(root)
	}
	abs := filepath.Join(absRoot, clean)
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil {
		return "", media.Wrap(media.StageScan, "resolve", err, "cannot relativise %q", ref).WithPath(root)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", media.New(media.StageScan, "resolve", "path %q escapes the input root", ref).WithPath(root)
	}
	realRoot, rerr := filepath.EvalSymlinks(absRoot)
	realAbs, aerr := filepath.EvalSymlinks(abs)
	if rerr == nil && aerr == nil {
		realRel, err := filepath.Rel(realRoot, realAbs)
		if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
			return "", media.New(media.StageScan, "resolve", "symlink %q escapes the input root", ref).WithPath(root)
		}
	}
	return filepath.ToSlash(rel), nil
}

// RootOf returns the directory that contains the manifest. Every input path
// must resolve inside it.
func RootOf(manifestPath string) string {
	return filepath.Dir(manifestPath)
}

// SegmentRefs lists every media segment reference in a parsed playlist.
func SegmentRefs(pl *manifest.Playlist) []SegmentRef {
	var refs []SegmentRef
	switch pl.Kind {
	case manifest.KindMedia:
		for _, s := range pl.Segments {
			refs = append(refs, SegmentRef{Index: s.Index, URI: s.URI, Rel: s.URI})
		}
	case manifest.KindDASH:
		for _, rep := range pl.Representations {
			for _, s := range rep.Segments {
				refs = append(refs, SegmentRef{Index: s.Index, URI: s.URI, Rel: s.URI})
			}
		}
	}
	return refs
}

// Input describes what to scan.
type Input struct {
	ManifestPath   string
	Playlist       *manifest.Playlist
	MediaRoot      string   // directory manifest URIs resolve against
	Metadata       []string // optional relative metadata files
	PlaylistDigest string   // SHA-256 of the bytes that were parsed
	Limits         Limits
}

// ErrIndexMissing marks a lookup that found no index on disk.
var ErrIndexMissing = errors.New("index missing")

// Scan hashes the manifest, every referenced segment and optional metadata,
// then returns a committed Index. Any failure returns no index at all.
func Scan(in Input) (*Index, error) {
	lim := in.Limits.resolved()
	root := in.MediaRoot
	if root == "" {
		root = RootOf(in.ManifestPath)
	}
	manDigest, err := HashFile(in.ManifestPath, lim.MaxSegmentBytes)
	if err != nil {
		return nil, err
	}
	if in.PlaylistDigest != "" && manDigest.SHA256 != in.PlaylistDigest {
		return nil, media.New(media.StageScan, "scan", "manifest changed while scanning (digest %s, parsed %s)", manDigest.SHA256, in.PlaylistDigest).WithPath(in.ManifestPath)
	}
	idx := &Index{
		Version: 1,
		Manifest: FileEntry{
			Kind: KindManifest, Path: filepath.ToSlash(filepath.Base(in.ManifestPath)),
			SHA256: manDigest.SHA256, Bytes: manDigest.Bytes,
		},
	}
	idx.TotalBytes = manDigest.Bytes

	seen := make(map[string]bool)
	add := func(kind Kind, uri, rel string, maxBytes int64) error {
		// The key includes the URI: one file can legitimately appear under
		// several URIs (init segment shared by representations), and each
		// reference must be independently verifiable.
		key := string(kind) + "\x00" + rel + "\x00" + uri
		if seen[key] {
			return nil
		}
		seen[key] = true
		if len(idx.Files) >= lim.MaxFiles {
			return media.New(media.StageScan, "scan", "file count exceeds limit %d", lim.MaxFiles)
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		d, err := HashFile(full, maxBytes)
		if err != nil {
			return err
		}
		if lim.ValidateContainers && (kind == KindMedia || kind == KindInit) && looksLikeMP4(rel) {
			if _, perr := media.ProbeFile(full, media.ProbeOptions{}); perr != nil {
				return perr
			}
		}
		if idx.TotalBytes+d.Bytes > lim.MaxTotalBytes {
			return media.New(media.StageScan, "scan", "total scan bytes exceed limit %d", lim.MaxTotalBytes).WithPath(full)
		}
		idx.TotalBytes += d.Bytes
		idx.Files = append(idx.Files, FileEntry{Kind: kind, Path: rel, URI: uri, SHA256: d.SHA256, Bytes: d.Bytes})
		return nil
	}

	for _, ref := range SegmentRefs(in.Playlist) {
		rel, err := Resolve(root, ref.Rel)
		if err != nil {
			return nil, err
		}
		if err := add(KindMedia, ref.URI, rel, lim.MaxSegmentBytes); err != nil {
			return nil, err
		}
	}
	if in.Playlist.Kind == manifest.KindDASH {
		for _, rep := range in.Playlist.Representations {
			if rep.InitURI == "" {
				continue
			}
			rel, err := Resolve(root, rep.InitURI)
			if err != nil {
				return nil, err
			}
			if err := add(KindInit, rep.InitURI, rel, lim.MaxSegmentBytes); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range in.Metadata {
		rel, err := Resolve(root, m)
		if err != nil {
			return nil, err
		}
		if err := add(KindMetadata, m, rel, lim.MaxSegmentBytes); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(idx.Files, func(i, j int) bool {
		if idx.Files[i].Kind != idx.Files[j].Kind {
			return idx.Files[i].Kind < idx.Files[j].Kind
		}
		return idx.Files[i].Path < idx.Files[j].Path
	})
	idx.Digest = indexDigest(idx)
	return idx, nil
}

// indexDigest is a stable SHA-256 over the index content, independent of
// JSON formatting and file-system ordering.
func indexDigest(idx *Index) string {
	h := sha256.New()
	fmt.Fprintf(h, "v%d\nmanifest\t%s\t%s\t%d\n", idx.Version, idx.Manifest.Path, idx.Manifest.SHA256, idx.Manifest.Bytes)
	for _, f := range idx.Files {
		fmt.Fprintf(h, "%s\t%s\t%s\t%s\t%d\n", f.Kind, f.Path, f.URI, f.SHA256, f.Bytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SegmentDigest finds the hashed entry for a resolved segment path, preferring
// an exact URI match so two URIs that resolve to one file stay distinguishable.
func (idx *Index) SegmentDigest(rel, uri string) (FileEntry, bool) {
	for _, f := range idx.Files {
		if f.Kind != KindMedia && f.Kind != KindInit {
			continue
		}
		if f.Path == rel && f.URI == uri {
			return f, true
		}
	}
	// A DASH manifest can reference the same file under two representations;
	// fall back to the path when the URI is not recorded verbatim.
	for _, f := range idx.Files {
		if f.Kind != KindMedia && f.Kind != KindInit {
			continue
		}
		if f.Path == rel {
			return f, true
		}
	}
	return FileEntry{}, false
}

// Save writes the index atomically to path.
func (idx *Index) Save(path string) error {
	if idx.Digest == "" {
		return media.New(media.StageScan, "save", "refusing to write an index without a digest")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return media.Wrap(media.StageScan, "save", err, "create index directory").WithPath(path)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return media.Wrap(media.StageScan, "save", err, "marshal index").WithPath(path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".index-*.tmp")
	if err != nil {
		return media.Wrap(media.StageScan, "save", err, "create temp index").WithPath(path)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return media.Wrap(media.StageScan, "save", err, "write temp index").WithPath(path)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return media.Wrap(media.StageScan, "save", err, "sync temp index").WithPath(path)
	}
	if err := tmp.Close(); err != nil {
		return media.Wrap(media.StageScan, "save", err, "close temp index").WithPath(path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return media.Wrap(media.StageScan, "save", err, "commit index").WithPath(path)
	}
	return nil
}

// Load reads an index, verifying its self digest. A corrupt, truncated or
// tampered index is reported as such; callers fall back to a full scan.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrIndexMissing
	}
	if err != nil {
		return nil, media.Wrap(media.StageScan, "load", err, "cannot open index").WithPath(path)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return nil, media.Wrap(media.StageScan, "load", err, "read index").WithPath(path)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, media.Wrap(media.StageScan, "load", err, "corrupt index JSON").WithPath(path)
	}
	if idx.Version != 1 || idx.Manifest.SHA256 == "" || len(idx.Files) == 0 {
		return nil, media.New(media.StageScan, "load", "index is incomplete").WithPath(path)
	}
	if want := indexDigest(&idx); want != idx.Digest {
		return nil, media.New(media.StageScan, "load", "index digest mismatch (want %s, got %s)", want, idx.Digest).WithPath(path)
	}
	return &idx, nil
}

// looksLikeMP4 reports whether a path names an ISO BMFF container.
func looksLikeMP4(rel string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(rel), ".")) {
	case "m4s", "mp4", "m4v", "m4a", "cmaf":
		return true
	}
	return false
}

// InputDigest is a stable digest of the file contents an index covers, used
// as the input half of a cache key.
func (idx *Index) InputDigest() string {
	h := sha256.New()
	fmt.Fprintf(h, "manifest\t%s\n", idx.Manifest.SHA256)
	for _, f := range idx.Files {
		fmt.Fprintf(h, "%s\t%s\t%s\n", f.Kind, f.Path, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
