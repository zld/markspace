package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/pion/rtp"

	"mediadist/timeline"
)

// SegmentRecord is one manifest entry for a segment of one representation.
type SegmentRecord struct {
	Index            int    `json:"index"`
	StartTicks       uint64 `json:"start_ticks"`
	DurationTicks    uint64 `json:"duration_ticks"`
	Bytes            uint64 `json:"bytes"`
	TargetBitrateBps uint64 `json:"target_bitrate_bps"`
	ActualBitrateBps uint64 `json:"actual_bitrate_bps"`
	ErrorBasisPts    uint64 `json:"error_basis_pts"`
	Switchable       bool   `json:"switchable"`
	// RTP fields come from pion/rtp and use the 90 kHz video clock directly.
	RTPStartSequence  uint16 `json:"rtp_start_sequence"`
	RTPStartTimestamp uint32 `json:"rtp_start_timestamp"`
}

// RepresentationRecord is the per-representation block of a manifest.
type RepresentationRecord struct {
	ID                   string          `json:"id"`
	TargetBitrateBps     uint64          `json:"target_bitrate_bps"`
	MeanActualBps        uint64          `json:"mean_actual_bitrate_bps"`
	BitrateErrorBasisPts uint64          `json:"bitrate_error_basis_pts"`
	QualityPSNRHunddB    uint64          `json:"quality_psnr_hundredths_db"`
	ColorSource          string          `json:"color_source"`
	AudioChannelsIn      int             `json:"audio_channels_in"`
	AudioChannelsOut     int             `json:"audio_channels_out"`
	SampleRateIn         uint32          `json:"sample_rate_in"`
	SampleRateOut        uint32          `json:"sample_rate_out"`
	Segments             []SegmentRecord `json:"segments"`
}

// Manifest is the published artifact. JSON serialization is canonical: no
// maps, ordered slices, fixed field tags, so repeated runs hash identically.
type Manifest struct {
	Version               int                    `json:"version"`
	CommonTimescale       uint32                 `json:"common_timescale"`
	TimelineEndTicks      uint64                 `json:"timeline_end_ticks"`
	ManifestDurationTicks uint64                 `json:"manifest_duration_ticks"`
	AlignmentErrorTicks   uint64                 `json:"alignment_error_ticks"`
	SegmentCount          int                    `json:"segment_count"`
	Boundaries            []BoundaryRecord       `json:"boundaries"`
	Representations       []RepresentationRecord `json:"representations"`
	SHA256                string                 `json:"sha256"`
}

// BoundaryRecord is the JSON view of a shared boundary.
type BoundaryRecord struct {
	AtTicks    uint64 `json:"at_ticks"`
	Switchable bool   `json:"switchable"`
	MaxDrift   uint64 `json:"max_drift_ticks"`
}

// AudioSummary carries the normalization figures shown per representation.
type AudioSummary struct {
	ChannelsIn, ChannelsOut int
	RateIn, RateOut         uint32
}

// BuildInput bundles everything the manifest builder needs.
type BuildInput struct {
	Reps         []Representation
	Plan         *Plan
	Reports      map[string]*BitrateReport
	ColorSources map[string]timeline.ColorSource
	Audio        map[string]AudioSummary
	SSRC         uint32
	PayloadType  uint8
}

// RTPPackager assigns RTP sequence numbers and timestamps to segment start
// points using pion/rtp. The 90 kHz common clock equals the RTP video clock,
// so segment start ticks map directly onto RTP timestamps; 32-bit and 16-bit
// wrap is handled by the uint types and exercised in tests.
type RTPPackager struct {
	ssrc        uint32
	payloadType uint8
	seq         uint16
}

// NewRTPPackager constructs a packager with a fixed starting sequence.
func NewRTPPackager(ssrc uint32, payloadType uint8) *RTPPackager {
	return &RTPPackager{ssrc: ssrc, payloadType: payloadType, seq: 0}
}

// packetFor mints the first RTP packet of a segment at the given common time.
func (p *RTPPackager) packetFor(startTicks uint64) (*rtp.Packet, uint16, uint32) {
	seq := p.seq
	ts := uint32(startTicks) // wrap at 2^32 is intentional and specified
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    p.payloadType,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           p.ssrc,
			Marker:         true,
		},
		Payload: []byte{0xAA},
	}
	p.seq++
	return pkt, seq, ts
}

// Build constructs the manifest, verifies duration conservation against the
// timeline end and computes a content hash over the canonical JSON body.
func Build(in BuildInput, cfg Config) (*Manifest, error) {
	if in.Plan == nil || len(in.Plan.Boundaries) < 2 {
		return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageManifest, 0, -1,
			"build", fmt.Errorf("plan has no boundaries"))
	}
	m := &Manifest{
		Version:          1,
		CommonTimescale:  timeline.CommonTimescale,
		TimelineEndTicks: in.Plan.End,
	}
	for _, b := range in.Plan.Boundaries {
		m.Boundaries = append(m.Boundaries, BoundaryRecord{
			AtTicks: b.At, Switchable: b.Switchable, MaxDrift: b.MaxDrift,
		})
	}
	var durSum uint64
	for i := 0; i < len(in.Plan.Boundaries)-1; i++ {
		durSum += in.Plan.Boundaries[i+1].At - in.Plan.Boundaries[i].At
	}
	m.ManifestDurationTicks = durSum
	m.AlignmentErrorTicks = absDiff64(durSum, in.Plan.End)
	if m.AlignmentErrorTicks > cfg.MaxDurationErrorTicks {
		return nil, timeline.NewLocError(timeline.CodeDurationConserv, timeline.StageManifest, 0, -1,
			"duration", fmt.Errorf("manifest=%d timeline=%d error=%d > %d",
				durSum, in.Plan.End, m.AlignmentErrorTicks, cfg.MaxDurationErrorTicks))
	}
	pkg := NewRTPPackager(in.SSRC, in.PayloadType)
	segCount := len(in.Plan.Boundaries) - 1
	m.SegmentCount = segCount
	for _, r := range in.Reps {
		rep, ok := in.Reports[r.ID]
		if !ok {
			return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageManifest, 0, -1,
				"build", fmt.Errorf("missing bitrate report for %s", r.ID))
		}
		rec := RepresentationRecord{
			ID:                   r.ID,
			TargetBitrateBps:     r.TargetBitrateBps,
			BitrateErrorBasisPts: rep.MeanErrorBasisPts,
			QualityPSNRHunddB:    EstimatePSNR(r.TargetBitrateBps).HundredthsDB,
			ColorSource:          string(in.ColorSources[r.ID]),
		}
		if a, ok := in.Audio[r.ID]; ok {
			rec.AudioChannelsIn, rec.AudioChannelsOut = a.ChannelsIn, a.ChannelsOut
			rec.SampleRateIn, rec.SampleRateOut = a.RateIn, a.RateOut
		}
		var actualSum uint64
		for _, st := range rep.Segments {
			pkt, seq, ts := pkg.packetFor(st.StartTicks)
			// Round-trip through wire encoding to prove RTP metadata is valid.
			raw, err := pkt.Marshal()
			if err != nil {
				return nil, timeline.NewLocError("RTP_MARSHAL", timeline.StageManifest, r.Video.ID,
					st.Index, "rtp", err)
			}
			var back rtp.Packet
			if err := back.Unmarshal(raw); err != nil {
				return nil, timeline.NewLocError("RTP_UNMARSHAL", timeline.StageManifest, r.Video.ID,
					st.Index, "rtp", err)
			}
			if back.SequenceNumber != seq || back.Timestamp != ts {
				return nil, timeline.NewLocError("RTP_MISMATCH", timeline.StageManifest, r.Video.ID,
					st.Index, "rtp", fmt.Errorf("wire roundtrip changed metadata"))
			}
			actualSum += st.ActualBitrateBps
			rec.Segments = append(rec.Segments, SegmentRecord{
				Index: st.Index, StartTicks: st.StartTicks, DurationTicks: st.DurationTicks,
				Bytes: st.Bytes, TargetBitrateBps: st.TargetBitrateBps,
				ActualBitrateBps: st.ActualBitrateBps, ErrorBasisPts: st.ErrorBasisPts,
				Switchable:       in.Plan.Boundaries[st.Index].Switchable,
				RTPStartSequence: seq, RTPStartTimestamp: ts,
			})
		}
		if len(rep.Segments) > 0 {
			rec.MeanActualBps = actualSum / uint64(len(rep.Segments))
		}
		m.Representations = append(m.Representations, rec)
	}
	sum, err := hashCanonical(m)
	if err != nil {
		return nil, err
	}
	m.SHA256 = sum
	return m, nil
}

// hashCanonical serializes without the hash field and returns its SHA-256.
func hashCanonical(m *Manifest) (string, error) {
	copy := *m
	copy.SHA256 = ""
	raw, err := json.MarshalIndent(&copy, "", "  ")
	if err != nil {
		return "", timeline.NewLocError("MANIFEST_ENCODE", timeline.StageManifest, 0, -1, "json", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func absDiff64(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// DurationMs renders manifest/timeline tick spans as milliseconds.
func DurationMs(ticks uint64) uint64 { return timeline.DurationMs(ticks) }
