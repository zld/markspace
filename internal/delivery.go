package kernel

import (
	"os"
	"strings"
)

const CacheFileName = "cache.json"

// CacheEntry records one source-specific segment derivative.
type CacheEntry struct {
	SourceDigest string `json:"source_digest"`
	SegmentID    string `json:"segment_id"`
	File         string `json:"file"`
	ContentHash  string `json:"content_hash"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

// CacheIndex is the bounded source-keyed derivative cache.
type CacheIndex struct {
	Version            int                   `json:"version"`
	ActiveSourceDigest string                `json:"active_source_digest"`
	Entries            map[string]CacheEntry `json:"entries"`
}

// CacheDiff reports terminal-visible reuse and invalidation counts.
type CacheDiff struct {
	Created     int `json:"created"`
	Reused      int `json:"reused"`
	Invalidated int `json:"invalidated"`
}

// CacheKey namespaces a segment by its complete source digest.
func CacheKey(sourceDigest, segmentID string) string {
	return sourceDigest + "/" + segmentID
}

// LoadCache reads cache.json or returns an empty cache when absent.
func LoadCache(out string) (*CacheIndex, string, error) {
	path, err := SafeJoin(out, CacheFileName)
	if err != nil {
		return nil, "", err
	}
	cache := &CacheIndex{Version: 1, Entries: map[string]CacheEntry{}}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return cache, path, nil
		}
		return nil, path, err
	}
	if err := ReadJSONLimited(path, MaxManifestBytes, cache); err != nil {
		return nil, path, err
	}
	if cache.Entries == nil {
		cache.Entries = map[string]CacheEntry{}
	}
	return cache, path, nil
}

// Save writes the cache atomically.
func (c *CacheIndex) Save(out string) (string, error) {
	path, err := SafeJoin(out, CacheFileName)
	if err != nil {
		return "", err
	}
	c.Version = 1
	return path, WriteJSONAtomic(path, c, MaxManifestBytes)
}

// InvalidateForSourceChange removes only entries keyed to the previous active source.
func (c *CacheIndex) InvalidateForSourceChange(newDigest, out string) int {
	old := c.ActiveSourceDigest
	if old == "" || old == newDigest {
		c.ActiveSourceDigest = newDigest
		return 0
	}
	prefix := old + "/"
	invalidated := 0
	for key, entry := range c.Entries {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		invalidated++
		if path, err := SafeJoin(out, entry.File); err == nil {
			_ = os.Remove(path)
		}
		delete(c.Entries, key)
	}
	c.ActiveSourceDigest = newDigest
	return invalidated
}

// DeliverSegments packages MP4 fragments and reuses valid source-keyed cache entries.
func DeliverSegments(out, sourceDigest string, segments []*Segment) (CacheDiff, error) {
	cache, _, err := LoadCache(out)
	if err != nil {
		return CacheDiff{}, err
	}
	diff := CacheDiff{Invalidated: cache.InvalidateForSourceChange(sourceDigest, out)}

	for _, seg := range segments {
		fp, err := SegmentFingerprint(seg)
		if err != nil {
			return diff, err
		}
		seg.ContentHash = fp
		short := sourceDigest
		if len(short) > 12 {
			short = short[:12]
		}
		seg.File = "segments/" + short + "/" + seg.ID + ".mp4"

		data, err := PackageSegmentMP4(seg, sourceDigest)
		if err != nil {
			return diff, err
		}
		fileHash := SHA256Hex(data)
		seg.FileSHA256 = fileHash
		seg.SizeBytes = int64(len(data))
		key := CacheKey(sourceDigest, seg.ID)

		if old, ok := cache.Entries[key]; ok && old.SHA256 == fileHash && old.File == seg.File {
			if diskHash, size, err := hashFile(mustJoin(out, seg.File)); err == nil && diskHash == fileHash && size == int64(len(data)) {
				diff.Reused++
				continue
			}
		}

		path, err := SafeJoin(out, seg.File)
		if err != nil {
			return diff, err
		}
		if err := WriteFileAtomic(path, data, 0o644); err != nil {
			return diff, err
		}
		cache.Entries[key] = CacheEntry{
			SourceDigest: sourceDigest,
			SegmentID:    seg.ID,
			File:         seg.File,
			ContentHash:  fp,
			SHA256:       fileHash,
			SizeBytes:    int64(len(data)),
		}
		diff.Created++
	}
	if _, err := cache.Save(out); err != nil {
		return diff, err
	}
	return diff, nil
}

func mustJoin(root, name string) string {
	p, err := SafeJoin(root, name)
	if err != nil {
		return name
	}
	return p
}
