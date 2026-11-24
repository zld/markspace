// Package pipeline owns incremental processing: content-addressed caching,
// crash-safe checkpoints and end-to-end provenance records.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape is returned when a requested output path escapes its root.
var ErrPathEscape = errors.New("path escapes its allowed root")

// filepathDir is a tiny wrapper around filepath.Dir kept in one place.
func filepathDir(path string) string { return filepath.Dir(path) }

// DigestSHA256 returns the hex SHA-256 of the given bytes.
func DigestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FileDigest returns the hex SHA-256 of a file's contents.
func FileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return DigestSHA256(data), nil
}

// SafeJoin joins root with clean relative parts and rejects any traversal
// that would resolve outside root. Every write in the kernel goes through it.
func SafeJoin(root string, parts ...string) (string, error) {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	joined := filepath.Join(append([]string{cleanRoot}, parts...)...)
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if abs != cleanRoot && !strings.HasPrefix(abs, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s not under %s", ErrPathEscape, abs, cleanRoot)
	}
	return abs, nil
}

// ToolVersions captures the external tools whose output must stay reproducible.
type ToolVersions struct {
	FFmpeg string `json:"ffmpeg"`
}

// CacheKey is the full identity of a processing request. The address covers
// all three identity components (input content, processing parameters and
// tool versions), so two different inputs can never alias one cache entry;
// the stored source digest is still independently re-verified on every hit.
type CacheKey struct {
	InputDigest string       `json:"input_digest"`
	Params      string       `json:"params"` // canonical JSON of processing params
	Versions    ToolVersions `json:"versions"`
}

// String renders the stable deterministic address text.
func (k CacheKey) String() string {
	return fmt.Sprintf("input=%s\nparams=%s\nffmpeg=%s\n", k.InputDigest, k.Params, k.Versions.FFmpeg)
}

// Hash returns the content address derived from the full request identity.
func (k CacheKey) Hash() string { return DigestSHA256([]byte(k.String())) }
