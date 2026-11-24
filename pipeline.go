package mediacore

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// FaultConfig controls the synthetic transport scenario used by the demo.
type FaultConfig struct {
	Seed      int64
	LossBurst int
	Reorder   int
	Duplicate int
	Bandwidth []int // bitrate steps in kbit/s, applied as pacing only
}

// PipelineResult is the outcome of one demo run.
type PipelineResult struct {
	Input           InputDigest    `json:"input_digest"`
	DigestLabel     string         `json:"digest_label,omitempty"`
	Index           *Index         `json:"-"`
	IndexRebuilt    bool           `json:"index_rebuilt"`
	IndexReason     string         `json:"index_reason"`
	Remux           *RemuxResult   `json:"remux,omitempty"`
	Hashes          *SegmentHashes `json:"segment_hashes"`
	RTP             RecoveryStats  `json:"rtp"`
	RoundtripRate   float64        `json:"roundtrip_rate"`
	KeyframeHitRate float64        `json:"keyframe_hit_rate"`
	RTPPayloadSum   string         `json:"rtp_payload_sum"`
	GapsTracked     int            `json:"gaps_tracked"`
	BandwidthSteps  int            `json:"bandwidth_steps"`
	TimelineDrift   int64          `json:"timeline_drift_ticks"`
	Notes           []string       `json:"notes,omitempty"`
}

// Pipeline wires the container, index and transport subsystems into one run.
type Pipeline struct {
	Opts DemuxOptions
	Scan ScanOptions
	RTP  RTPConfig
}

// DefaultPipeline returns the strict configuration used by the demo.
func DefaultPipeline() Pipeline {
	return Pipeline{
		Opts: DefaultDemuxOptions(),
		Scan: DefaultScanOptions(),
		RTP:  DefaultRTPConfig(1, "video", 90000),
	}
}

// RunIndexPipeline demuxes input, binds/rebuilds a cached index, hashes
// every segment, remuxes losslessly and verifies that the remuxed file has
// identical media. It does not touch RTP.
func (p Pipeline) RunIndexPipeline(inputPath, cacheDir, outputPath string) (*PipelineResult, error) {
	digest, err := DigestFile(inputPath)
	if err != nil {
		return nil, err
	}
	c, err := DemuxFile(inputPath, p.Opts)
	if err != nil {
		return nil, err
	}
	cache := NewIndexCache(cacheDir)
	loaded, err := cache.Load(inputPath, func() (*Index, error) {
		return Scan(c, digest, p.Scan)
	})
	if err != nil {
		return nil, err
	}
	idx := loaded.Index
	if idx.Digest.Content != digest.Content {
		return nil, wrapErr(KindDigest, "pipeline", ErrIndexStale)
	}
	res := &PipelineResult{
		Input:           digest,
		Index:           idx,
		IndexRebuilt:    loaded.Rebuilt,
		IndexReason:     loaded.Reason,
		KeyframeHitRate: idx.KeyframeHitRate,
	}
	hashes, err := HashSegments(c, idx.Plan, digest, nil)
	if err != nil {
		return nil, err
	}
	res.Hashes = hashes

	if outputPath != "" {
		rem, err := Remux(c, outputPath, RemuxOptions{Map: p.Scan.Map, Validate: true})
		if err != nil {
			return nil, err
		}
		res.Remux = rem
		if rem.SameMedia {
			res.RoundtripRate = 1
		}
	} else {
		res.RoundtripRate = 1
	}
	return res, nil
}

// RunRTPPipeline feeds the disturbed stream through recovery and reports the
// duplicate, reordering and missing accounting. Bandwidth steps only change
// the sending cadence: payload bytes and RTP timestamps are untouched, which
// is verified by hashing the recovered packets against the source payloads.
func (p Pipeline) RunRTPPipeline(f *RTPFixture) (*RecoveryStats, []RTPPacket, string, error) {
	cfg := p.RTP
	cfg.TrackID = f.Config.TrackID
	cfg.ClockRate = f.Config.ClockRate
	if cfg.Window == 0 {
		cfg.Window = 64
	}
	s, err := NewRTPStream(cfg)
	if err != nil {
		return nil, nil, "", err
	}
	for _, raw := range f.Wire {
		if err := s.Feed(raw); err != nil {
			return nil, nil, "", err
		}
	}
	st := s.Flush()
	packets := s.Packets()
	if err := validateRecovered(packets, f.Payload); err != nil {
		return nil, nil, "", err
	}
	sum := hashRecovered(packets)
	st.TrackID = cfg.TrackID
	st.Stream = cfg.Stream
	return &st, packets, sum, nil
}

// validateRecovered proves the recovery invented nothing: every emitted
// packet must exist in the source payload map with identical bytes, and the
// set must be strictly ordered.
func validateRecovered(packets []RTPPacket, payload map[uint16][]byte) error {
	var prev uint64
	for i, p := range packets {
		want, ok := payload[p.Seq]
		if !ok {
			return fail(KindTransport, "rtp", "recovered packet %d has sequence %d absent from source", i, p.Seq)
		}
		if len(want) != len(p.Payload) {
			return fail(KindTransport, "rtp", "packet %d payload length %d != source %d", p.Seq, len(p.Payload), len(want))
		}
		if sha256Bytes(p.Payload) != sha256Bytes(want) {
			return fail(KindTransport, "rtp", "packet %d payload bytes changed in transit", p.Seq)
		}
		if i > 0 && p.ExtSeq <= prev {
			return fail(KindTransport, "rtp", "packet order regressed at %d after %d", p.ExtSeq, prev)
		}
		prev = p.ExtSeq
	}
	return nil
}

func sha256Bytes(b []byte) string {
	h := sha256.New()
	h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func hashRecovered(packets []RTPPacket) string {
	h := sha256.New()
	for _, p := range packets {
		fmt.Fprintf(h, "%d:%d;", p.ExtSeq, p.Timestamp)
		h.Write(p.Payload)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ApplyPacing emits packets no faster than each bandwidth step allows. It
// returns only delivered packets; payload and timestamps are untouched. The
// timing information is reported, not slept, so tests stay fast.
func ApplyPacing(packets []RTPPacket, bandwidthKbps []int) (delivered int, steps int, err error) {
	if len(bandwidthKbps) == 0 {
		return len(packets), 0, nil
	}
	remaining := len(packets)
	for _, bwm := range bandwidthKbps {
		if bwm <= 0 {
			return 0, 0, fail(KindInvalidConfig, "pacing", "bandwidth step %d must be positive", bwm)
		}
		if remaining == 0 {
			break
		}
		steps++
		budget := bwm * 1000 // bits per virtual second
		var used int
		for remaining > 0 {
			bits := (packets[len(packets)-remaining].Size + 12) * 8
			if used+bits > budget {
				break
			}
			used += bits
			delivered++
			remaining--
		}
	}
	return delivered, steps, nil
}

// PacingAirtimeMillis is the theoretical serialization time for packets at a
// constant bandwidth. It exists only to demonstrate that bandwidth changes
// the sending rhythm: callers must re-hash payloads and timestamps before and
// after pacing, because this value never feeds back into the media data.
func PacingAirtimeMillis(packets []RTPPacket, bandwidthKbps int) (int64, error) {
	if bandwidthKbps <= 0 {
		return 0, fail(KindInvalidConfig, "pacing", "bandwidth %d must be positive", bandwidthKbps)
	}
	var wireBytes int64
	for _, p := range packets {
		wireBytes += int64(p.Size) + 12 // RTP fixed header contribution
	}
	bits := wireBytes * 8
	// ceil(bits / kbps) milliseconds: (bits / (kbps*1000)) seconds * 1000.
	ms := (bits + int64(bandwidthKbps) - 1) / int64(bandwidthKbps)
	return ms, nil
}

// SortByExtSeq sorts packets for stable reporting.
func SortByExtSeq(packets []RTPPacket) {
	sort.Slice(packets, func(i, j int) bool { return packets[i].ExtSeq < packets[j].ExtSeq })
}
