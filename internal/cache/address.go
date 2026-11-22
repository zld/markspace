// Package cache implements content-addressed stage results with provenance
// verification. A cache entry is only trusted when its key, provenance, stage
// version and stored content digest all agree; anything else is quarantined.
package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Limits bound every cache allocation and path traversal.
const (
	MaxEntryBytes    = 4 << 20
	MaxManifestBytes = 64 << 10
	MaxPayloadBytes  = 4 << 20
	MaxKeyBytes      = 256
)

var (
	// ErrKeyFormat reports a cache key that is not a 64-char lowercase hex SHA-256.
	ErrKeyFormat = errors.New("cache: invalid content key")
	// ErrProvenance reports a provenance mismatch between caller and entry.
	ErrProvenance = errors.New("cache: provenance mismatch")
	// ErrVersion reports an entry written by another processing version.
	ErrVersion = errors.New("cache: processing version mismatch")
	// ErrContentDigest reports a stored content digest that does not match.
	ErrContentDigest = errors.New("cache: content digest mismatch")
	// ErrInputDigest reports an input digest disagreement.
	ErrInputDigest = errors.New("cache: input digest mismatch")
	// ErrQuarantined reports an entry that has been isolated.
	ErrQuarantined = errors.New("cache: entry quarantined")
	// ErrMissing reports an absent entry.
	ErrMissing = errors.New("cache: entry missing")
	// ErrPath reports a path escape attempt.
	ErrPath = errors.New("cache: path outside cache root")
)

var keyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Key is a validated content address.
type Key string

// NewKey validates a hex-encoded SHA-256 content address.
func NewKey(hexDigest string) (Key, error) {
	if len(hexDigest) > MaxKeyBytes || !keyPattern.MatchString(hexDigest) {
		return "", fmt.Errorf("%w: %q", ErrKeyFormat, truncate(hexDigest, 80))
	}
	return Key(hexDigest), nil
}

// KeyFromBytes derives a content key from arbitrary bytes.
func KeyFromBytes(b []byte) Key {
	sum := sha256.Sum256(b)
	return Key(hex.EncodeToString(sum[:]))
}

// String returns the hex form.
func (k Key) String() string { return string(k) }

// Valid reports whether the key is a well-formed address.
func (k Key) Valid() bool { return keyPattern.MatchString(string(k)) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Source identifies who produced an input and where it came from.
type Source struct {
	ID      string `json:"id"`      // stable producer identity, e.g. "edge-a/rtp"
	Digest  Key    `json:"digest"`  // SHA-256 of the produced bytes
	Bytes   int64  `json:"bytes"`   // size, cross-checked against the payload
	Locator string `json:"locator"` // human-readable origin for diagnostics
}

// Validate checks a source record.
func (s Source) Validate() error {
	if strings.TrimSpace(s.ID) == "" || len(s.ID) > 128 {
		return fmt.Errorf("%w: source id %q", ErrProvenance, truncate(s.ID, 40))
	}
	if !s.Digest.Valid() {
		return fmt.Errorf("%w: source digest %q", ErrProvenance, truncate(s.Digest.String(), 80))
	}
	if len(s.Locator) > 256 {
		return fmt.Errorf("%w: locator too long", ErrProvenance)
	}
	if s.Bytes < 0 || s.Bytes > MaxEntryBytes {
		return fmt.Errorf("%w: source bytes %d", ErrProvenance, s.Bytes)
	}
	return nil
}

// Manifest is the auditable header of one cache entry.
type Manifest struct {
	Format       string `json:"format"`        // always "mediapipe-cache/1"
	Stage        string `json:"stage"`         // pipeline stage name
	Version      string `json:"version"`       // processing version
	Key          Key    `json:"key"`           // content address of Result
	Input        Source `json:"input"`         // upstream product
	ResultSource Source `json:"result_source"` // this stage's producer identity
	Content      Key    `json:"content"`       // SHA-256 of the stored result bytes
	ResultBytes  int64  `json:"result_bytes"`
}

const manifestFormat = "mediapipe-cache/1"

// Store is a directory-backed content-addressed cache.
type Store struct {
	root string
}

// Open creates (or validates) a cache root directory.
func Open(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cache: root %q is not a directory", root)
	}
	return &Store{root: abs}, nil
}

// Root returns the absolute cache root.
func (s *Store) Root() string { return s.root }

func (s *Store) pathFor(key Key, suffix string) (string, error) {
	if !key.Valid() {
		return "", fmt.Errorf("%w: %q", ErrKeyFormat, truncate(key.String(), 80))
	}
	p := filepath.Join(s.root, string(key[:2]), string(key)+suffix)
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(s.root, abs); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q", ErrPath, p)
	}
	return abs, nil
}

// QuarantineReason is a stable code carried by a quarantined entry.
type QuarantineReason string

const (
	ReasonProvenance QuarantineReason = "provenance"
	ReasonVersion    QuarantineReason = "version"
	ReasonContent    QuarantineReason = "content"
	ReasonInput      QuarantineReason = "input"
	ReasonFormat     QuarantineReason = "format"
	ReasonMissing    QuarantineReason = "missing"
)

// Quarantine records why an entry was isolated.
type Quarantine struct {
	Key    Key
	Reason QuarantineReason
	Detail string
}

func (q *Quarantine) Error() string {
	return fmt.Sprintf("cache: quarantined %s (%s): %s", truncate(string(q.Key), 16), q.Reason, q.Detail)
}

// Lookup verifies an entry against the caller's provenance expectations.
// It returns the result bytes only when every check passes.
type Lookup struct {
	Key     Key
	Stage   string
	Version string
	Input   Source
	Now     int64
}

// Result is a verified cache hit.
type Result struct {
	Bytes    []byte
	Manifest Manifest
	Hit      bool
}

// Fetch reads and verifies an entry. On any mismatch the entry files are moved
// to quarantine and the mismatch is returned; the caller must recompute.
func (s *Store) Fetch(l Lookup) (Result, error) {
	if err := l.Input.Validate(); err != nil {
		return Result{}, err
	}
	manPath, err := s.pathFor(l.Key, ".manifest.json")
	if err != nil {
		return Result{}, err
	}
	raw, err := readLimited(manPath, MaxManifestBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("%w: %s", ErrMissing, truncate(string(l.Key), 16))
		}
		return Result{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Result{}, s.quarantine(l, ReasonFormat, fmt.Sprintf("manifest decode: %v", err))
	}
	if m.Format != manifestFormat {
		return Result{}, s.quarantine(l, ReasonFormat, "format "+truncate(m.Format, 40))
	}
	if m.Key != l.Key {
		return Result{}, s.quarantine(l, ReasonContent, "manifest key mismatch")
	}
	if m.Stage != l.Stage {
		return Result{}, s.quarantine(l, ReasonProvenance, "stage "+m.Stage+" != "+l.Stage)
	}
	if m.Version != l.Version {
		return Result{}, s.quarantine(l, ReasonVersion, m.Version+" != "+l.Version)
	}
	if m.Input.ID != l.Input.ID || m.Input.Digest != l.Input.Digest {
		return Result{}, s.quarantine(l, ReasonInput, "input provenance differs")
	}
	if m.Input.Bytes != l.Input.Bytes {
		return Result{}, s.quarantine(l, ReasonInput, "input byte count differs")
	}
	if err := m.ResultSource.Validate(); err != nil {
		return Result{}, s.quarantine(l, ReasonProvenance, err.Error())
	}
	resPath, err := s.pathFor(l.Key, ".bin")
	if err != nil {
		return Result{}, err
	}
	body, err := readLimited(resPath, MaxEntryBytes)
	if err != nil {
		return Result{}, s.quarantine(l, ReasonContent, "result read: "+err.Error())
	}
	if int64(len(body)) != m.ResultBytes {
		return Result{}, s.quarantine(l, ReasonContent, fmt.Sprintf("result bytes %d != manifest %d", len(body), m.ResultBytes))
	}
	if KeyFromBytes(body) != m.Content {
		return Result{}, s.quarantine(l, ReasonContent, "result digest mismatch")
	}
	return Result{Bytes: body, Manifest: m, Hit: true}, nil
}

// Store writes a verified entry atomically. The key must equal the digest of
// the result bytes; the manifest is written after the payload.
func (s *Store) Store(manifest Manifest, result []byte) error {
	if manifest.Format != manifestFormat {
		return fmt.Errorf("cache: invalid format %q", manifest.Format)
	}
	if !manifest.Key.Valid() {
		return fmt.Errorf("%w: %q", ErrKeyFormat, truncate(manifest.Key.String(), 80))
	}
	if len(result) > MaxEntryBytes {
		return fmt.Errorf("cache: result %d bytes exceeds limit", len(result))
	}
	if err := manifest.Input.Validate(); err != nil {
		return err
	}
	if err := manifest.ResultSource.Validate(); err != nil {
		return err
	}
	// Key is the caller's address for the entry, which may be derived from the
	// stage's inputs rather than from its output. Content is always the digest
	// of the stored bytes, and the producing source must declare exactly that
	// digest; anything else is refused.
	content := KeyFromBytes(result)
	if manifest.ResultSource.Digest != content {
		return fmt.Errorf("%w: result source digest != content digest", ErrProvenance)
	}
	if manifest.Content != "" && manifest.Content != content {
		return fmt.Errorf("%w: declared content digest mismatch", ErrContentDigest)
	}
	manifest.Content = content
	manifest.ResultBytes = int64(len(result))
	resPath, err := s.pathFor(manifest.Key, ".bin")
	if err != nil {
		return err
	}
	manPath, err := s.pathFor(manifest.Key, ".manifest.json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resPath), 0o755); err != nil {
		return err
	}
	enc, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(enc) > MaxManifestBytes {
		return fmt.Errorf("cache: manifest %d bytes exceeds limit", len(enc))
	}
	if err := writeAtomic(resPath, result, 0o644); err != nil {
		return err
	}
	return writeAtomic(manPath, enc, 0o644)
}

func (s *Store) quarantine(l Lookup, reason QuarantineReason, detail string) error {
	q := &Quarantine{Key: l.Key, Reason: reason, Detail: detail}
	dir := filepath.Join(s.root, "quarantine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return q
	}
	base := filepath.Join(dir, string(l.Key)+"."+string(reason))
	if manPath, err := s.pathFor(l.Key, ".manifest.json"); err == nil {
		_ = os.Rename(manPath, base+".manifest.json")
	}
	if resPath, err := s.pathFor(l.Key, ".bin"); err == nil {
		_ = os.Rename(resPath, base+".bin")
	}
	return q
}

// Quarantined lists isolated entries for reporting.
func (s *Store) Quarantined() ([]string, error) {
	dir := filepath.Join(s.root, "quarantine")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

func readLimited(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("cache: file %q is %d bytes, limit %d", filepath.Base(path), info.Size(), limit)
	}
	buf := make([]byte, info.Size())
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cachetmp-*")
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
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// DigestParts derives a key over labelled byte parts, so stage inputs can be
// composed unambiguously.
func DigestParts(parts ...[]byte) Key {
	h := sha256.New()
	var lenBuf [4]byte
	for _, p := range parts {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(p)))
		h.Write(lenBuf[:])
		h.Write(p)
	}
	return Key(hex.EncodeToString(h.Sum(nil)))
}
