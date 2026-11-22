// Package ingest turns raw local assets into typed, offset-annotated records.
package ingest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediad/internal/core"
)

// Limits bounds every read performed by the ingest stage. They exist so a
// damaged or hostile asset cannot trigger unbounded allocation.
type Limits struct {
	MaxAssetBytes int64
	MaxAssets     int
}

// DefaultLimits are suitable for short live segments and their sidecars.
func DefaultLimits() Limits {
	return Limits{MaxAssetBytes: 16 << 20, MaxAssets: 512}
}

// Asset is one immutable input file plus its content digest.
type Asset struct {
	Name   string // slash-separated path relative to the stream root
	Format string // srt, vtt, mp4, rtp, timecode, bin
	Data   []byte
	Span   core.Span
	Digest string
}

// Size returns the asset length in bytes.
func (a *Asset) Size() int64 { return int64(len(a.Data)) }

// Resolve joins rel to root and rejects traversal, absolute paths, and
// symlinks that escape the root. The returned path is absolute.
func Resolve(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("ingest: empty asset path")
	}
	if strings.ContainsRune(rel, 0) {
		return "", errors.New("ingest: NUL in asset path")
	}
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("ingest: path %q escapes the stream root", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(absRoot, rel)
	// Check the lexical path first so a missing file cannot hide traversal.
	if err := ensureWithin(absRoot, filepath.Clean(candidate)); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if err := ensureWithin(absRoot, real); err != nil {
		return "", err
	}
	return real, nil
}

func ensureWithin(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ingest: path %q escapes the stream root", target)
	}
	return nil
}

// Load reads one asset from disk while enforcing the configured byte cap.
func Load(root, rel string, limits Limits) (*Asset, error) {
	path, err := Resolve(root, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadAsset(f, filepath.ToSlash(rel), limits)
}

// ReadAsset reads at most limits.MaxAssetBytes+1 bytes so an oversized
// stream is detected without trusting a length header.
func ReadAsset(r io.Reader, name string, limits Limits) (*Asset, error) {
	if limits.MaxAssetBytes <= 0 {
		limits = DefaultLimits()
	}
	buf := make([]byte, 0, 64*1024)
	chunk := make([]byte, 32*1024)
	var total int64
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			total += int64(n)
			if total > limits.MaxAssetBytes {
				return nil, fmt.Errorf("ingest: asset %q exceeds %d bytes", name, limits.MaxAssetBytes)
			}
			buf = append(buf, chunk[:n]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return &Asset{
		Name:   name,
		Format: FormatOf(name),
		Data:   buf,
		Span:   core.Span{Start: 0, End: total},
		Digest: core.HashParts(buf),
	}, nil
}

// FormatOf derives the asset format from the file extension.
func FormatOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".srt":
		return "srt"
	case ".vtt":
		return "vtt"
	case ".mp4", ".m4s", ".m4v", ".mov":
		return "mp4"
	case ".rtp", ".rtpdump":
		return "rtp"
	case ".tc", ".timecode":
		return "timecode"
	default:
		return "bin"
	}
}

// InputDigest hashes the ordered asset names and their content digests. It is
// the identity a checkpoint binds to, so it must not depend on mtimes.
func InputDigest(assets []*Asset) string {
	parts := make([][]byte, 0, len(assets)*2)
	for _, a := range assets {
		parts = append(parts, []byte(a.Name), []byte(a.Digest))
	}
	return core.HashParts(parts...)
}

// Sizes returns the asset size table used to validate spans.
func Sizes(assets []*Asset) map[string]int64 {
	m := make(map[string]int64, len(assets))
	for _, a := range assets {
		m[a.Name] = a.Size()
	}
	return m
}

// LoadManifest loads each named asset and returns them in manifest order.
func LoadManifest(root string, names []string, limits Limits) ([]*Asset, error) {
	if len(names) > limits.MaxAssets {
		return nil, fmt.Errorf("ingest: %d assets exceeds limit %d", len(names), limits.MaxAssets)
	}
	out := make([]*Asset, 0, len(names))
	for _, name := range names {
		a, err := Load(root, name, limits)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
