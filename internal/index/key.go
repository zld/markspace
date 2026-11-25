// Package index builds and persists integer-frame random-access time indexes.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"assetcheck/internal/errmodel"
	"assetcheck/internal/stream"
)

// SchemaVersion is the on-disk index schema identifier.
const SchemaVersion = "assetcheck-index/v1"

// Key is one random-access point: exact frame, integer nanosecond PTS and the
// byte offset of its block in the WAV data chunk/file.
type Key struct {
	Frame      int64 `json:"frame"`
	Nanos      int64 `json:"nanos"`
	ByteOffset int64 `json:"byte-offset"`
	BlockIndex int   `json:"block-index"`
}

// Index is the deterministic, content-addressed time index payload.
type Index struct {
	Schema              string      `json:"schema"`
	ContentFingerprint  string      `json:"content-fingerprint"`
	MetadataFingerprint string      `json:"metadata-fingerprint"`
	SampleRate          int         `json:"sample-rate"`
	Channels            int         `json:"channels"`
	TotalFrames         int64       `json:"total-frames"`
	MaxIntervalFrames   int64       `json:"max-interval-frames"`
	CorruptRanges       []rangeJSON `json:"corrupt-ranges,omitempty"`
	Keys                []Key       `json:"keys"`
	Digest              string      `json:"digest"`
}

// rangeJSON mirrors mediaclock.Range without importing the type into the
// on-disk schema naming directly (kept explicit for stable JSON tags).
type rangeJSON struct {
	Start int64 `json:"start-frame"`
	End   int64 `json:"end-frame"`
}

// Build creates the index from a scan report and fingerprints.
func Build(rep *stream.Report, contentFP, metadataFP string) (*Index, error) {
	idx := &Index{
		Schema:              SchemaVersion,
		ContentFingerprint:  contentFP,
		MetadataFingerprint: metadataFP,
		SampleRate:          rep.SampleRate,
		Channels:            rep.Channels,
		TotalFrames:         rep.TotalFrames,
		MaxIntervalFrames:   rep.Interval.MaxFrames,
	}
	for _, r := range rep.CorruptRanges {
		idx.CorruptRanges = append(idx.CorruptRanges, rangeJSON{Start: r.Start, End: r.End})
	}
	offsetByStart := map[int64]stream.BlockVerdict{}
	for _, b := range rep.Blocks {
		offsetByStart[b.Range.Start] = b
	}
	for _, f := range rep.Keyframes {
		k := Key{Frame: f, Nanos: rep.Clock.FramesToNanos(f)}
		if bv, ok := offsetByStart[f]; ok {
			k.ByteOffset = bv.ByteOffset
			k.BlockIndex = bv.Index
		}
		idx.Keys = append(idx.Keys, k)
	}
	if err := idx.Validate(); err != nil {
		return nil, err
	}
	d, err := idx.computeDigest()
	if err != nil {
		return nil, err
	}
	idx.Digest = d
	return idx, nil
}

type digestShape struct {
	Schema              string      `json:"schema"`
	ContentFingerprint  string      `json:"content-fingerprint"`
	MetadataFingerprint string      `json:"metadata-fingerprint"`
	SampleRate          int         `json:"sample-rate"`
	Channels            int         `json:"channels"`
	TotalFrames         int64       `json:"total-frames"`
	MaxIntervalFrames   int64       `json:"max-interval-frames"`
	CorruptRanges       []rangeJSON `json:"corrupt-ranges,omitempty"`
	Keys                []Key       `json:"keys"`
}

func (idx *Index) computeDigest() (string, error) {
	b, err := json.Marshal(digestShape{
		Schema: idx.Schema, ContentFingerprint: idx.ContentFingerprint,
		MetadataFingerprint: idx.MetadataFingerprint, SampleRate: idx.SampleRate,
		Channels: idx.Channels, TotalFrames: idx.TotalFrames,
		MaxIntervalFrames: idx.MaxIntervalFrames, CorruptRanges: idx.CorruptRanges,
		Keys: idx.Keys,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Validate checks schema, monotonic integer PTS and absence of duplicate keys.
func (idx *Index) Validate() error {
	if idx.Schema != SchemaVersion {
		return errmodel.New(errmodel.ErrIndexSchema, "unexpected index schema %q", idx.Schema)
	}
	var lastFrame int64 = -1
	var lastNanos int64 = -1
	seen := map[int64]bool{}
	for i, k := range idx.Keys {
		if seen[k.Frame] {
			return errmodel.New(errmodel.ErrDuplicateKey,
				"duplicate index key at frame %d (position %d)", k.Frame, i).AtFrame(k.Frame)
		}
		seen[k.Frame] = true
		if k.Frame < lastFrame {
			return errmodel.New(errmodel.ErrIndexSchema, "keys not sorted at position %d", i).AtFrame(k.Frame)
		}
		if k.Nanos < lastNanos {
			return errmodel.New(errmodel.ErrIndexSchema, "PTS not monotonic at frame %d", k.Frame).AtFrame(k.Frame)
		}
		lastFrame = k.Frame
		lastNanos = k.Nanos
	}
	return nil
}

// VerifyDigest recomputes the canonical digest.
func (idx *Index) VerifyDigest() error {
	want, err := idx.computeDigest()
	if err != nil {
		return err
	}
	if want != idx.Digest {
		return errmodel.New(errmodel.ErrIndexChecksum,
			"index digest mismatch: stored=%s recomputed=%s", idx.Digest, want)
	}
	return nil
}

// AtOrBefore returns the last keyframe not after frame f.
func (idx *Index) AtOrBefore(frame int64) (Key, bool) {
	var got Key
	ok := false
	for _, k := range idx.Keys {
		if k.Frame > frame {
			break
		}
		got, ok = k, true
	}
	return got, ok
}

// Marshal serializes the index deterministically.
func (idx *Index) Marshal() ([]byte, error) {
	return json.MarshalIndent(idx, "", "  ")
}

// UnmarshalIndex parses and verifies an index.
func UnmarshalIndex(b []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, errmodel.New(errmodel.ErrIndexSchema, "invalid index JSON: %v", err)
	}
	if err := idx.Validate(); err != nil {
		return nil, err
	}
	if err := idx.VerifyDigest(); err != nil {
		return nil, err
	}
	return &idx, nil
}
