package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/zld/media-check/media"
)

// Build produces the manifest in index order. Failed entries never alter the
// recorded fields of successful ones.
func (m *Merger) Build(extra Manifest) (*Manifest, error) {
	results, _ := m.Results()
	man := Manifest{
		Version:   1,
		Seed:      m.seed,
		Source:    m.source,
		SourceSHA: m.srcSHA,
		Stats:     m.stats,
		Tracks:    extra.Tracks,
		Segments:  m.total,
		RTP:       extra.RTP,
		Index:     extra.Index,
	}
	for _, r := range results {
		e := ManifestEntry{
			Index: r.Index, TrackID: r.TrackID, Status: r.Status,
			FirstSample: r.Segment.First, SampleCount: r.Segment.Count,
			DTSStart: r.Segment.DTSStart, Duration: r.Segment.Duration,
			Bytes: r.Segment.ByteSize, SHA256: r.SHA256,
			Artifact: r.ArtifactPath, ArtifactSHA: r.ArtifactSHA,
			Resumed: r.Resumed, ErrClass: r.ErrClass, Err: r.ErrText,
		}
		switch r.Status {
		case StatusOK:
		case StatusFailed:
			man.Failed++
			// scrub payload evidence from failed rows so a failure cannot be
			// mistaken for verified content.
			e.SHA256 = ""
			e.Artifact = ""
			e.ArtifactSHA = ""
		case StatusCancelled:
			man.Cancelled++
			e.SHA256 = ""
			e.Artifact = ""
			e.ArtifactSHA = ""
		}
		man.Entries = append(man.Entries, e)
	}
	man.Complete = man.Cancelled == 0 && man.Failed == 0 && len(m.Missing()) == 0 && m.total == len(results)
	sha, err := manifestDigest(&man)
	if err != nil {
		return nil, err
	}
	man.SHA256 = sha
	return &man, nil
}

// Invariants validates cross-stage guarantees on the ordered manifest.
// With expected == nil only structural rules are enforced (suitable for a
// cancelled run); otherwise sample-count conservation is also checked.
func (m *Manifest) Invariants(expected map[uint32]uint32) error {
	counts := make(map[uint32]uint32)
	last := make(map[uint32]ManifestEntry)
	for _, e := range m.Entries {
		if e.Status != StatusOK {
			continue
		}
		if prev, ok := last[e.TrackID]; ok {
			if e.FirstSample != prev.FirstSample+prev.SampleCount {
				return media.NewError(media.ClassIntegrity, "invariant.boundary", 0,
					"track %d segment %d starts at %d, expected %d", e.TrackID, e.Index, e.FirstSample, prev.FirstSample+prev.SampleCount).WithTrack(e.TrackID).WithSegment(e.Index)
			}
			if e.DTSStart != prev.DTSStart+prev.Duration {
				return media.NewError(media.ClassIntegrity, "invariant.dts", 0,
					"track %d segment %d DTS %d does not continue %d+%d", e.TrackID, e.Index, e.DTSStart, prev.DTSStart, prev.Duration).WithTrack(e.TrackID).WithSegment(e.Index)
			}
		}
		if e.SampleCount == 0 {
			return media.NewError(media.ClassIntegrity, "invariant.count", 0, "segment %d has zero samples", e.Index).WithSegment(e.Index)
		}
		last[e.TrackID] = e
		counts[e.TrackID] += e.SampleCount
	}
	if expected == nil {
		return nil
	}
	for track, want := range expected {
		if got := counts[track]; got != want {
			return media.NewError(media.ClassIntegrity, "invariant.count", 0,
				"track %d accounted %d samples, expected %d", track, got, want).WithTrack(track)
		}
	}
	return nil
}

// String is a compact summary used in terminal output.
func (m *Manifest) String() string {
	return fmt.Sprintf("manifest segments=%d ok=%d failed=%d cancelled=%d sha=%s",
		len(m.Entries), len(m.Entries)-m.Failed-m.Cancelled, m.Failed, m.Cancelled, m.SHA256[:12])
}

// manifestDigest hashes the canonical projection of the manifest. Execution
// provenance (absolute paths, pool sizing, whether an entry was resumed) is
// excluded so that the same inputs always yield the same digest, while every
// piece of content evidence is covered.
func manifestDigest(m *Manifest) (string, error) {
	type canonEntry struct {
		Index       int         `json:"index"`
		TrackID     uint32      `json:"track_id"`
		Status      Status      `json:"status"`
		FirstSample uint32      `json:"first_sample"`
		SampleCount uint32      `json:"sample_count"`
		DTSStart    uint64      `json:"dts_start"`
		Duration    uint64      `json:"duration"`
		Bytes       int64       `json:"bytes"`
		SHA256      string      `json:"sha256"`
		Artifact    string      `json:"artifact"`
		ArtifactSHA string      `json:"artifact_sha256"`
		ErrClass    media.Class `json:"error_class,omitempty"`
	}
	type canon struct {
		Version   int          `json:"version"`
		Seed      int64        `json:"seed"`
		SourceSHA string       `json:"source_sha256"`
		Tracks    int          `json:"tracks"`
		Segments  int          `json:"segments"`
		Cancelled int          `json:"cancelled_tasks"`
		Failed    int          `json:"failed_tasks"`
		Complete  bool         `json:"complete"`
		RTP       *RTPReport   `json:"rtp,omitempty"`
		Index     *IndexReport `json:"index,omitempty"`
		Entries   []canonEntry `json:"entries"`
	}
	c := canon{
		Version: m.Version, Seed: m.Seed, SourceSHA: m.SourceSHA,
		Tracks: m.Tracks, Segments: m.Segments, Cancelled: m.Cancelled,
		Failed: m.Failed, Complete: m.Complete, RTP: m.RTP, Index: m.Index,
	}
	for _, e := range m.Entries {
		c.Entries = append(c.Entries, canonEntry{
			Index: e.Index, TrackID: e.TrackID, Status: e.Status,
			FirstSample: e.FirstSample, SampleCount: e.SampleCount,
			DTSStart: e.DTSStart, Duration: e.Duration, Bytes: e.Bytes,
			SHA256: e.SHA256, Artifact: e.Artifact, ArtifactSHA: e.ArtifactSHA,
			ErrClass: e.ErrClass,
		})
	}
	b, err := json.Marshal(&c)
	if err != nil {
		return "", media.WrapError(media.ClassIntegrity, "manifest.json", 0, err, "marshal manifest")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Digest recomputes the manifest digest.
func (m *Manifest) Digest() (string, error) { return manifestDigest(m) }

// VerifyManifest re-hashes and compares against the stored SHA.
func (m *Manifest) VerifyManifest() error {
	want, err := m.Digest()
	if err != nil {
		return err
	}
	if m.SHA256 != want {
		return media.NewError(media.ClassIntegrity, "manifest.verify", 0, "manifest digest mismatch: stored=%s computed=%s", m.SHA256, want)
	}
	return nil
}
