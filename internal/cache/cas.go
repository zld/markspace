// Package cache stores derived segments in a content-and-parameter-addressed
// cache. Unlike the immutable source store, a derived key depends on the track
// *content* digest plus the processing parameters — not the source file hash —
// so a container/metadata-only edit that leaves sample bytes untouched keeps
// reusing the same derived segments.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// cacheBufSize bounds every streaming read/write in the cache.
const cacheBufSize = 64 * 1024

// ErrChecksum means a stored derived segment failed its SHA-256 verification.
// Callers must treat it as a cache miss, isolate the segment and recompute.
var ErrChecksum = errors.New("cache: derived segment checksum mismatch")

// Params are the processing parameters that participate in the derived key.
// Two segments share a cache entry only when both the track content and the
// processing parameters are identical.
type Params struct {
	TargetKbps int    `json:"target_kbps"`
	Pipeline   string `json:"pipeline_version"`
}

// ByteRange is one contiguous slice of a track inside a source blob.
type ByteRange struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// Store is the derived-segment cache rooted at root/derived.
type Store struct {
	root string
}

// New creates or opens a derived cache.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "derived"), 0o755); err != nil {
		return nil, fmt.Errorf("cache: create store: %w", err)
	}
	return &Store{root: root}, nil
}

// DerivedKey addresses a derived segment from the track content digest and
// processing parameters. Domain separators keep the inputs unambiguous.
func DerivedKey(trackContentHash string, p Params) (string, error) {
	if trackContentHash == "" {
		return "", errors.New("cache: empty track content hash")
	}
	canonical, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("cache: marshal params: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "track=%s\x00params=%s", trackContentHash, canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) pathFor(key string) string {
	return filepath.Join(s.root, "derived", key[:2], key)
}

func (s *Store) sidecarFor(key string) string {
	return s.pathFor(key) + ".sha256"
}

// Has reports whether a verified entry for key exists. A corrupt entry (bad
// checksum or missing sidecar) is reported as absent so callers recompute.
func (s *Store) Has(key string) bool {
	_, err := s.verify(key)
	return err == nil
}

// Put atomically writes a derived segment and its checksum sidecar. The write
// goes through a temp file and is renamed into place, so a crash never leaves
// a partially written entry visible to readers.
func (s *Store) Put(key string, data []byte) error {
	dst := s.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])

	tmp, err := os.CreateTemp(filepath.Dir(dst), "seg-*.tmp")
	if err != nil {
		return fmt.Errorf("cache: temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("cache: finalize: %w", err)
	}
	// Sidecar after the data is in place; readers without it treat as a miss.
	return os.WriteFile(s.sidecarFor(key), []byte(checksum), 0o644)
}

// Get returns the verified path of a cached segment or ErrChecksum.
func (s *Store) Get(key string) (string, error) {
	return s.verify(key)
}

func (s *Store) verify(key string) (string, error) {
	dst := s.pathFor(key)
	wantBytes, err := os.ReadFile(s.sidecarFor(key))
	if err != nil {
		return "", fmt.Errorf("cache: sidecar: %w", err)
	}
	f, err := os.Open(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, cacheBufSize)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != string(wantBytes) {
		return "", ErrChecksum
	}
	return dst, nil
}

// SumHex returns the full SHA-256 hex digest of an in-memory byte slice. It is
// used to record the checksum of a recomputed derived segment in manifests.
func SumHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TrackContentHash streams the byte ranges of one track out of a source blob
// and hashes exactly the sample content. Because it reads sample bytes rather
// than container boxes, an unchanged track inside a re-wrapped container still
// produces the same digest and therefore reuses its derived cache.
func TrackContentHash(ra io.ReaderAt, ranges []ByteRange) (string, error) {
	h := sha256.New()
	buf := make([]byte, cacheBufSize)
	for _, r := range ranges {
		remaining := r.Length
		pos := r.Offset
		for remaining > 0 {
			n := min(int64(len(buf)), remaining)
			m, err := ra.ReadAt(buf[:n], pos)
			if m > 0 {
				h.Write(buf[:m])
				pos += int64(m)
				remaining -= int64(m)
			}
			if err == io.EOF {
				if remaining > 0 || m == 0 {
					return "", io.ErrUnexpectedEOF
				}
				break
			}
			if err != nil {
				return "", fmt.Errorf("cache: read track range: %w", err)
			}
			if m == 0 {
				return "", io.ErrUnexpectedEOF
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
