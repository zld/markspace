package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"archivecheck/internal"
)

// Snapshot is a fresh reread of an upstream segment or event payload.
type Snapshot struct {
	SourceID string
	Data     []byte
}

// ReadFunc rereads a source instead of trusting the cached copy.
type ReadFunc func(context.Context) (Snapshot, error)

// Entry is the content-addressed cache metadata.
type Entry struct {
	Digest    string   `json:"digest"`
	Size      int64    `json:"size"`
	SourceIDs []string `json:"source_ids"`
	Path      string   `json:"path"`
}

// Result reports verification and whether a rejected entry was reingested.
type Result struct {
	Entry          Entry  `json:"entry"`
	CacheHit       bool   `json:"cache_hit"`
	SourceVerified bool   `json:"source_verified"`
	Reingested     bool   `json:"reingested"`
	Reason         string `json:"reason,omitempty"`
}

// CAS is a SHA-256 content-addressed local store.
type CAS struct {
	root string
}

func New(root string) (*CAS, error) {
	if root == "" {
		return nil, internal.NewFault("cache.init", root, 0, "cache root is empty")
	}
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o755); err != nil {
		return nil, internal.WrapFault("cache.init", root, 0, err)
	}
	return &CAS{root: root}, nil
}

func (c *CAS) objectPath(digest string) (string, error) {
	if len(digest) != 64 {
		return "", internal.NewFault("cache.digest", "", 0, "invalid SHA-256 digest length %d", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", internal.WrapFault("cache.digest", "", 0, err)
	}
	return filepath.Join(c.root, "objects", digest[:2], digest[2:]), nil
}

// Put atomically stores data by SHA-256 and records its source identifier.
func (c *CAS) Put(ctx context.Context, sourceID string, data []byte) (*Entry, error) {
	digest := SHA256Hex(data)
	path, err := c.objectPath(digest)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, internal.WrapFault("cache.put", sourceID, 0, err)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := atomicWrite(path, data); err != nil {
			return nil, internal.WrapFault("cache.put", sourceID, 0, err)
		}
	} else if err != nil {
		return nil, internal.WrapFault("cache.put", sourceID, 0, err)
	}

	entry := Entry{Digest: digest, Size: int64(len(data)), Path: path}
	_ = c.updateMeta(&entry, sourceID)
	return &entry, nil
}

// Ensure always rereads the source. A cache hit is reused only when both the
// source identifier and SHA-256 verify. Any mismatch causes fresh ingestion.
func (c *CAS) Ensure(ctx context.Context, expectedSourceID, claimedDigest string, read ReadFunc) (*Result, []byte, error) {
	if read == nil {
		return nil, nil, internal.NewFault("cache.verify", expectedSourceID, 0, "nil source reader")
	}
	snap, err := read(ctx)
	if err != nil {
		return nil, nil, internal.WrapFault("cache.reread", expectedSourceID, 0, err)
	}
	if snap.SourceID == "" {
		snap.SourceID = expectedSourceID
	}

	actualDigest := SHA256Hex(snap.Data)
	sourceMismatch := expectedSourceID != "" && snap.SourceID != expectedSourceID
	digestMismatch := claimedDigest != "" && claimedDigest != actualDigest

	if sourceMismatch || digestMismatch {
		reasons := make([]string, 0, 2)
		if sourceMismatch {
			reasons = append(reasons, fmt.Sprintf("source id %q != %q", snap.SourceID, expectedSourceID))
		}
		if digestMismatch {
			reasons = append(reasons, "sha256 mismatch")
		}
		entry, perr := c.Put(ctx, snap.SourceID, snap.Data)
		if perr != nil {
			return nil, nil, perr
		}
		return &Result{
			Entry: *entry, SourceVerified: false, Reingested: true,
			Reason: strings.Join(reasons, "; "),
		}, snap.Data, nil
	}

	digest := actualDigest
	if claimedDigest != "" {
		digest = claimedDigest
	}
	path, err := c.objectPath(digest)
	if err != nil {
		return nil, nil, err
	}
	cached, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, internal.WrapFault("cache.read", expectedSourceID, 0, err)
	}

	res := &Result{SourceVerified: true}
	if err == nil {
		res.CacheHit = true
		if SHA256Hex(cached) != digest {
			// The cached object itself is damaged. Refuse it and replace from source.
			entry, perr := c.Put(ctx, snap.SourceID, snap.Data)
			if perr != nil {
				return nil, nil, perr
			}
			res.Entry = *entry
			res.CacheHit = false
			res.Reingested = true
			res.SourceVerified = false
			res.Reason = "cached object sha256 mismatch"
			return res, snap.Data, nil
		}
		res.Entry = Entry{Digest: digest, Size: int64(len(cached)), Path: path}
		_ = c.updateMeta(&res.Entry, snap.SourceID)
		return res, cached, nil
	}

	entry, err := c.Put(ctx, snap.SourceID, snap.Data)
	if err != nil {
		return nil, nil, err
	}
	res.Entry = *entry
	return res, snap.Data, nil
}

func (c *CAS) updateMeta(e *Entry, sourceID string) error {
	metaPath := e.Path + ".json"
	var old Entry
	if b, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(b, &old)
		e.SourceIDs = old.SourceIDs
	}
	if sourceID != "" && !contains(e.SourceIDs, sourceID) {
		e.SourceIDs = append(e.SourceIDs, sourceID)
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(metaPath, b)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
