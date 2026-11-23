// Package export renders an off-screen, deterministic frame manifest from a
// committed timeline. It never decodes pictures: each frame record carries
// timestamps, metadata and a content hash, and every exported segment is
// checksummed so identical inputs produce identical manifests across runs.
package export

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediaroute/internal/clock"
	"mediaroute/internal/timeline"
)

// FrameRecord is one deterministic, JSON-serializable manifest entry.
type FrameRecord struct {
	Ordinal   int    `json:"ordinal"`
	Track     uint32 `json:"track"`
	Codec     string `json:"codec"`
	Sample    uint32 `json:"sample"`
	Usec      int64  `json:"usec"`
	RTPTS     uint32 `json:"rtp_timestamp"`
	Keyframe  bool   `json:"keyframe"`
	SegmentID string `json:"segment_id"`
	FrameSHA  string `json:"frame_sha256"`
}

// SegmentDigest groups frames of one committed segment with a hash covering
// the exact ordered frame records it contains.
type SegmentDigest struct {
	SegmentID  string   `json:"segment_id"`
	FrameCount int      `json:"frame_count"`
	SHA256     string   `json:"segment_sha256"`
	Frames     []string `json:"frame_sha256"`
}

// Manifest is the full export artifact.
type Manifest struct {
	Segments []SegmentDigest `json:"segments"`
	Records  []FrameRecord   `json:"records"`
}

// hashBytes returns the hex SHA-256 of data.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// canonicalRecord renders a frame record as the stable byte form used for
// segment hashing. JSON field order is fixed by struct declaration.
func canonicalRecord(r FrameRecord) ([]byte, error) {
	return json.Marshal(r)
}

// Build constructs a manifest from timeline frames. All frames must carry a
// non-empty SegmentID; anything uncommitted is refused rather than exported.
func Build(frames []*timeline.Frame) (*Manifest, error) {
	bySegment := map[string][]*timeline.Frame{}
	var segOrder []string
	records := make([]FrameRecord, 0, len(frames))
	for i, fr := range frames {
		if fr.SegmentID == "" {
			return nil, clock.E(clock.PhaseExport, fr.Track, -1, false,
				"refusing to export frame without committed segment", nil)
		}
		if _, ok := bySegment[fr.SegmentID]; !ok {
			segOrder = append(segOrder, fr.SegmentID)
		}
		bySegment[fr.SegmentID] = append(bySegment[fr.SegmentID], fr)
		records = append(records, FrameRecord{
			Ordinal:   i,
			Track:     fr.Track,
			Codec:     string(fr.Codec),
			Sample:    fr.Sample,
			Usec:      fr.Usec,
			RTPTS:     fr.RTPTS,
			Keyframe:  fr.Keyframe,
			SegmentID: fr.SegmentID,
			FrameSHA:  hashBytes(fr.Bytes),
		})
	}
	sort.Strings(segOrder)
	manifest := &Manifest{Records: records}
	for _, seg := range segOrder {
		segFrames := bySegment[seg]
		digest := SegmentDigest{SegmentID: seg, FrameCount: len(segFrames)}
		// Hash in timeline order so the same input always hashes the same.
		var segRecords []FrameRecord
		for _, rec := range records {
			if rec.SegmentID != seg {
				continue
			}
			digest.Frames = append(digest.Frames, rec.FrameSHA)
			segRecords = append(segRecords, rec)
		}
		sum, err := hashSegment(segRecords)
		if err != nil {
			return nil, clock.E(clock.PhaseExport, 0, -1, false,
				"manifest record encoding failed", err)
		}
		digest.SHA256 = sum
		manifest.Segments = append(manifest.Segments, digest)
	}
	return manifest, nil
}

// JSON serializes the manifest deterministically (no map iteration, fixed
// struct order, indented for human inspection).
func (m *Manifest) JSON() ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, clock.E(clock.PhaseExport, 0, -1, false, "manifest encode failed", err)
	}
	return raw, nil
}

// ContentHash hashes the whole manifest's canonical JSON, giving the verify
// command a single repeat-hash value to compare across runs.
func (m *Manifest) ContentHash() (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", clock.E(clock.PhaseExport, 0, -1, false, "manifest hash failed", err)
	}
	return hashBytes(raw), nil
}

// hashSegment digests one segment's ordered frame records with the exact
// rule Build uses. RecomputeSegmentHashes exposes it so a caller can verify
// published digests without duplicating the rule.
func hashSegment(records []FrameRecord) (string, error) {
	h := sha256.New()
	for _, rec := range records {
		raw, err := canonicalRecord(rec)
		if err != nil {
			return "", err
		}
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RecomputeSegmentHashes rebuilds every segment digest from the manifest's
// own records. A caller that has independently re-validated each frame's
// bytes can compare the result with SegmentDigest.SHA256 to prove the
// published digest still describes the committed content.
func (m *Manifest) RecomputeSegmentHashes() (map[string]string, error) {
	bySegment := map[string][]FrameRecord{}
	for _, rec := range m.Records {
		bySegment[rec.SegmentID] = append(bySegment[rec.SegmentID], rec)
	}
	out := make(map[string]string, len(bySegment))
	for seg, recs := range bySegment {
		h, err := hashSegment(recs)
		if err != nil {
			return nil, clock.E(clock.PhaseExport, 0, -1, false,
				"manifest record encoding failed", err)
		}
		out[seg] = h
	}
	return out, nil
}

// SegmentHashes returns segment id -> sha256 in manifest order.
func (m *Manifest) SegmentHashes() map[string]string {
	out := make(map[string]string, len(m.Segments))
	for _, d := range m.Segments {
		out[d.SegmentID] = d.SHA256
	}
	return out
}
