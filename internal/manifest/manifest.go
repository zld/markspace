// Package manifest renders the verifiable per-segment manifest. The output
// is deterministic so repeated runs over the same input produce an
// identical manifest digest; each segment carries an explicit conclusion
// (aligned/failed) plus track coverage, RTP loss/jitter and per-layer skew.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/local/tracecheck/internal/align"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/local/tracecheck/internal/rtptrace"
)

// SegmentReport is one manifest line item.
type SegmentReport struct {
	Index         int            `json:"index"`
	StartNS       int64          `json:"start_ns"`
	EndNS         int64          `json:"end_ns"`
	Status        string         `json:"status"` // "aligned" or "failed"
	FailCode      string         `json:"fail_code,omitempty"`
	SkewNS        int64          `json:"skew_ns"`
	TrackCoverage map[uint64]int `json:"track_coverage"`
	SourceOffsets []int64        `json:"source_offsets"`
	ContentSHA256 string         `json:"content_sha256,omitempty"`
	CacheKey      string         `json:"cache_key,omitempty"`
}

// TrackReport summarizes coverage and RTP health per track.
type TrackReport struct {
	TrackNumber uint64 `json:"track_number"`
	CodecID     string `json:"codec_id,omitempty"`
	Blocks      int    `json:"blocks"`
	LostPackets int    `json:"lost_packets"`
	JitterNS    int64  `json:"jitter_ns"`
	MaxJitterNS int64  `json:"max_jitter_ns"`
}

// AnomalyReport renders a timestamp anomaly with its locator.
type AnomalyReport struct {
	Code         string `json:"code"`
	TrackNumber  uint64 `json:"track_number"`
	RawTimecode  int64  `json:"raw_timecode"`
	AbsTimeNS    int64  `json:"abs_time_ns"`
	SourceOffset int64  `json:"source_offset"`
}

// Manifest is the complete, deterministically serializable report.
type Manifest struct {
	Version        int             `json:"version"`
	InputID        string          `json:"input_id"`
	SegmentCount   int             `json:"segment_count"`
	AlignedCount   int             `json:"aligned_count"`
	MaxSkewNS      int64           `json:"max_skew_ns"`
	PacketLoss     int             `json:"packet_loss"`
	QueuePeak      int             `json:"queue_peak"`
	CacheHits      int             `json:"cache_hits"`
	Tracks         []TrackReport   `json:"tracks"`
	Segments       []SegmentReport `json:"segments"`
	Anomalies      []AnomalyReport `json:"anomalies"`
	FailedRanges   [][2]int64      `json:"failed_ranges"`
	ManifestSHA256 string          `json:"manifest_sha256"`
}

// Inputs bundles everything the manifest needs.
type Inputs struct {
	InputID   string
	Doc       *mkv.Document
	Trace     *rtptrace.Trace
	Result    *align.Result
	QueuePeak int
	CacheHits int
	// SegmentContent maps segment index to (content hash, cache key).
	SegmentContent map[int]ContentRef
}

// ContentRef binds a segment to its digest and cache key.
type ContentRef struct {
	SHA256   string
	CacheKey string
}

// Build constructs the manifest and computes its own digest. The digest is
// computed over the canonical JSON with the digest field blank, so it is
// stable across repeated builds of identical content.
func Build(in Inputs) *Manifest {
	m := &Manifest{
		Version:      1,
		InputID:      in.InputID,
		SegmentCount: len(in.Result.Segments),
		AlignedCount: in.Result.Aligned,
		MaxSkewNS:    int64(in.Result.MaxSkewNS),
		PacketLoss:   in.Trace.TotalLost(),
		QueuePeak:    in.QueuePeak,
		CacheHits:    in.CacheHits,
		FailedRanges: in.Result.FailedRanges,
	}

	// Track coverage and RTP health.
	trackBlocks := map[uint64]int{}
	for _, b := range in.Doc.Blocks {
		trackBlocks[b.TrackNumber]++
	}
	for _, tr := range in.Doc.Tracks {
		rep := TrackReport{
			TrackNumber: tr.Number,
			CodecID:     tr.CodecID,
			Blocks:      trackBlocks[tr.Number],
		}
		if st := in.Trace.Stats[tr.Number]; st != nil {
			rep.LostPackets = st.LostPackets
			rep.JitterNS = int64(st.JitterNS)
			rep.MaxJitterNS = int64(st.MaxJitterNS)
		}
		m.Tracks = append(m.Tracks, rep)
	}

	// Per-segment conclusions.
	for _, seg := range in.Result.Segments {
		rep := SegmentReport{
			Index:         seg.Index,
			StartNS:       seg.StartNS,
			EndNS:         seg.EndNS,
			SkewNS:        int64(seg.SkewNS),
			TrackCoverage: seg.LayerCoverage,
			SourceOffsets: seg.BlockSourceOffsets,
		}
		if seg.OK {
			rep.Status = "aligned"
		} else {
			rep.Status = "failed"
			rep.FailCode = string(seg.FailCode)
		}
		if ref, ok := in.SegmentContent[seg.Index]; ok {
			rep.ContentSHA256 = ref.SHA256
			rep.CacheKey = ref.CacheKey
		}
		m.Segments = append(m.Segments, rep)
	}

	// Anomalies in source order.
	for _, a := range in.Doc.Anomalies {
		m.Anomalies = append(m.Anomalies, AnomalyReport{
			Code:         string(a.Code),
			TrackNumber:  a.TrackNumber,
			RawTimecode:  a.RawTimecode,
			AbsTimeNS:    int64(a.AbsTime),
			SourceOffset: a.SourceOffset,
		})
	}

	m.ManifestSHA256 = Digest(m)
	return m
}

// Digest hashes the canonical manifest JSON with the digest field blank.
func Digest(m *Manifest) string {
	cp := *m
	cp.ManifestSHA256 = ""
	raw, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Marshal renders the manifest including its digest as indented JSON.
func (m *Manifest) Marshal() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
