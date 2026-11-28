package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"mediad/index"
	"mediad/timeline"
)

// Manifest is the reviewable output of a batch run. It records the time range,
// the renditions, the input fingerprints, every anomaly, and a per-segment
// digest, so a reviewer can audit exactly what was published and from which
// bytes. A manifest with any fatal issue is never published: Publish refuses.
type Manifest struct {
	// Schema is the manifest format version.
	Schema int `json:"schema"`
	// JobID is a stable short id derived from the inputs and config.
	JobID string `json:"job_id"`
	// Complete is false for a partially committed run.
	Complete bool `json:"complete"`
	// StartUs and EndUs bound the published media on the shared timeline.
	StartUs int64 `json:"start_us"`
	EndUs   int64 `json:"end_us"`
	// Renditions lists each rendition's name and time base.
	Renditions []RenditionInfo `json:"renditions"`
	// Inputs maps path to its three-way fingerprint.
	Inputs map[string]index.Fingerprint `json:"inputs"`
	// Events are the published timed metadata, sorted on the shared timeline.
	Events []EventInfo `json:"events,omitempty"`
	// Segments are the aligned segments, chunked for checkpointing.
	Segments []SegmentInfo `json:"segments"`
	// Issues are all anomalies, fatal or not. Fatal ones block publication.
	Issues []timeline.Issue `json:"issues,omitempty"`
	// Stats are the acceptance figures the demo reports.
	Stats Stats `json:"stats"`
	// Digest is the manifest's self-checksum over its canonical content. It is
	// computed last and excluded from its own hash input.
	Digest string `json:"digest,omitempty"`
}

// RenditionInfo summarises a rendition in the manifest.
type RenditionInfo struct {
	Name      string `json:"name"`
	TrackID   uint32 `json:"track_id"`
	TimeScale uint32 `json:"timescale"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Samples   int    `json:"samples"`
}

// EventInfo is a published metadata event with its source and payload digest.
type EventInfo struct {
	Kind       string          `json:"kind"`
	Scheme     string          `json:"scheme,omitempty"`
	TimeUs     int64           `json:"time_us"`
	DurationUs int64           `json:"duration_us,omitempty"`
	Source     timeline.Source `json:"source"`
	PayloadSum string          `json:"payload_sha256"`
}

// SegmentInfo is one manifest segment with per-rendition byte ranges.
type SegmentInfo struct {
	Index      int                         `json:"index"`
	StartUs    int64                       `json:"start_us"`
	EndUs      int64                       `json:"end_us"`
	Renditions map[string]SegmentByteRange `json:"renditions"`
	Digest     string                      `json:"digest"`
}

// SegmentByteRange pins a rendition's bytes for one segment.
type SegmentByteRange struct {
	FirstSample     int   `json:"first_sample"`
	LastSample      int   `json:"last_sample"`
	Offset          int64 `json:"offset"`
	Length          int64 `json:"length"`
	BoundaryErrorUs int64 `json:"boundary_error_us"`
}

// Stats are the run figures. They are computed from the generated sample only.
type Stats struct {
	AVSyncMs         float64 `json:"av_sync_ms"`
	LossRecoveryRate float64 `json:"loss_recovery_rate"`
	LatencyP95Ms     float64 `json:"latency_p95_ms"`
	AlignedSegments  int     `json:"aligned_segments"`
	IndexReused      bool    `json:"index_reused"`
	EventCount       int     `json:"event_count"`
	SegmentCount     int     `json:"segment_count"`
	// MaxBoundaryErrorUs is the worst keyframe offset from a cut across every
	// rendition and segment; it must stay within one video frame.
	MaxBoundaryErrorUs int64 `json:"max_boundary_error_us"`
}

// HasFatalIssues reports whether any issue must block publication.
func (m *Manifest) HasFatalIssues() bool {
	for _, i := range m.Issues {
		if i.Severity != timeline.SeverityWarning {
			return true
		}
	}
	return false
}

// Publish finalises and returns the manifest, or refuses when incomplete or
// carrying fatal issues. It computes the digest over canonical content so two
// runs that produce the same output produce the same digest regardless of map
// iteration order or how chunks were checkpointed.
func (m *Manifest) Publish() ([]byte, error) {
	if !m.Complete {
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeManifest,
			Message: "refusing to publish an incomplete manifest"}}
	}
	if m.HasFatalIssues() {
		fatal := 0
		for _, i := range m.Issues {
			if i.Severity != timeline.SeverityWarning {
				fatal++
			}
		}
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeManifest,
			Message: fmt.Sprintf("refusing to publish: %d fatal issue(s); see manifest issues", fatal)}}
	}
	sort.SliceStable(m.Segments, func(i, j int) bool { return m.Segments[i].Index < m.Segments[j].Index })
	m.Digest = m.computeDigest()
	return json.MarshalIndent(m, "", "  ")
}

// CanonicalDigest returns the digest a published manifest would carry, without
// publishing. It is used to compare a resumed run against a continuous one.
func (m *Manifest) CanonicalDigest() string {
	sorted := *m
	sorted.Segments = append([]SegmentInfo(nil), m.Segments...)
	sort.SliceStable(sorted.Segments, func(i, j int) bool { return sorted.Segments[i].Index < sorted.Segments[j].Index })
	return sorted.computeDigest()
}

// computeDigest hashes the parts of the manifest that must be reproducible:
// time range, renditions, input fingerprints, events, segments and stats. It
// deliberately excludes Created-time-like fields and its own Digest.
func (m *Manifest) computeDigest() string {
	h := sha256.New()
	var buf [8]byte
	writeStr := func(s string) {
		binary.BigEndian.PutUint64(buf[:], uint64(len(s)))
		h.Write(buf[:])
		h.Write([]byte(s))
	}
	writeInt := func(v int64) {
		binary.BigEndian.PutUint64(buf[:], uint64(v))
		h.Write(buf[:])
	}
	writeStr("mediad-manifest-v1")
	writeInt(int64(m.Schema))
	writeStr(m.JobID)
	writeInt(m.StartUs)
	writeInt(m.EndUs)
	for _, r := range m.Renditions {
		writeStr(r.Name)
		writeInt(int64(r.TrackID))
		writeInt(int64(r.TimeScale))
		writeInt(int64(r.Samples))
	}
	for _, path := range sortedKeys(m.Inputs) {
		writeStr(path)
		writeStr(m.Inputs[path].Content)
		writeStr(m.Inputs[path].Container)
		writeStr(m.Inputs[path].Metadata)
	}
	for _, e := range m.Events {
		writeStr(e.Kind)
		writeStr(e.Scheme)
		writeInt(e.TimeUs)
		writeInt(e.DurationUs)
		writeStr(e.Source.File)
		writeInt(e.Source.ByteOffset)
		writeInt(int64(e.Source.TrackID))
		writeStr(e.PayloadSum)
	}
	for _, s := range m.Segments {
		writeInt(int64(s.Index))
		writeInt(s.StartUs)
		writeInt(s.EndUs)
		for _, name := range sortedKeys(s.Renditions) {
			r := s.Renditions[name]
			writeStr(name)
			writeInt(int64(r.FirstSample))
			writeInt(int64(r.LastSample))
			writeInt(r.Offset)
			writeInt(r.Length)
			writeInt(r.BoundaryErrorUs)
		}
	}
	writeInt(int64(m.Stats.AlignedSegments))
	writeInt(int64(m.Stats.SegmentCount))
	writeInt(int64(m.Stats.EventCount))
	return hex.EncodeToString(h.Sum(nil))
}

// SegmentDigest hashes one segment's byte ranges; the pipeline stores it on the
// segment so a single segment's integrity can be checked without the manifest.
func SegmentDigest(s SegmentInfo) string {
	h := sha256.New()
	var buf [8]byte
	for _, name := range sortedKeys(s.Renditions) {
		r := s.Renditions[name]
		h.Write([]byte(name))
		binary.BigEndian.PutUint64(buf[:], uint64(r.Offset))
		h.Write(buf[:])
		binary.BigEndian.PutUint64(buf[:], uint64(r.Length))
		h.Write(buf[:])
		binary.BigEndian.PutUint64(buf[:], uint64(r.BoundaryErrorUs))
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
