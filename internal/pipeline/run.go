// Package pipeline wires the media kernel together.
//
// Packets enter through the RTP reorder layer, loss is repaired where parity
// allows, and surviving packets are released through the jitter buffer on a
// shared integer timeline. The sync stage maps both tracks onto one clock, the
// container adapter supplies per-sample durations, sync flags, and codec
// configuration, and the index and segment stages produce deterministic
// boundaries and hashes.
//
// Every stage shares the error model in internal/merrors: each recorded
// problem carries its stage, a category, and where possible a packet
// sequence number or byte offset.
package pipeline

import (
	"encoding/binary"
	"sort"

	"example.com/mediaforge/internal/adapter"
	"example.com/mediaforge/internal/clock"
	"example.com/mediaforge/internal/merrors"
	"example.com/mediaforge/internal/segment"
	"example.com/mediaforge/internal/syncer"
)

// ParityPayloadType marks an RTP packet carrying FEC parity rather than media.
const ParityPayloadType = 100

// FrameSize is the fixed protected size of one media packet: a 4-byte RTP
// timestamp, a 2-byte payload length, and the payload padded with zeros. XOR
// parity needs equal lengths, and carrying the timestamp through parity means
// a recovered packet lands at its real place on the timeline instead of being
// guessed from its neighbours.
const FrameSize = 4 + 2 + 96

// MaxRecordedErrors bounds the per-report error list.
const MaxRecordedErrors = 200

// RawPacket is one packet as it arrived on the wire.
type RawPacket struct {
	Arrival uint64 // reference arrival time in reference-clock ticks
	Bytes   []byte // complete RTP packet
}

// TrackInput describes one input stream.
type TrackInput struct {
	Kind        syncer.Kind
	SSRC        uint32
	ClockRate   uint32
	OffsetTicks int64
	// MP4TrackID ties this stream to a track in the container fixture.
	MP4TrackID uint32
	// StartSeq is the sequence number of the first media packet. Packet k of
	// the stream has sequence StartSeq+k.
	StartSeq uint16
	Packets  []RawPacket
}

// SenderReportInput is one RTCP Sender Report with the extended RTP stamp the
// device claimed at that instant.
type SRInput struct {
	Report clock.SenderReport
	RTPExt uint64
}

// Input is a complete scenario.
type Input struct {
	MP4           []byte
	TruncatedMP4  []byte
	Tracks        []TrackInput
	SenderReports []SRInput

	ReorderWindow      int
	MaxBuffer          int
	JitterWindowTicks  uint64
	SegmentCount       int
	MaxCorrectionTicks uint64
	ReferenceRate      uint32
	FECGroupSize       int
}

// ContractReport is passed through the multipliers.
type ContractReport struct {
	Violations []string
}

// Report is the outcome of one run.
type Report struct {
	RoundtripRate   float64
	KeyframeHitRate float64
	SegmentCount    int
	SegmentHashes   []segment.Hash
	Recovered       int
	UnrecoveredGaps int
	Late            int
	Duplicates      int
	ClockWraps      int
	DriftPPM        int64
	Truncated       int
	OutOfSync       int

	ExpectedSamples  int
	DeliveredSamples int
	TimelineEnd      uint64

	// RemuxBytes is the size of the re-muxed container.
	RemuxBytes int
	Errors     []error
}

// Record stores a non-fatal error, keeping the list bounded.
func (r *Report) Record(err error) {
	if err == nil || len(r.Errors) >= MaxRecordedErrors {
		return
	}
	r.Errors = append(r.Errors, err)
}

// Frame serialises a media packet for FEC protection.
func Frame(timestamp uint32, payload []byte) ([]byte, error) {
	if len(payload) > FrameSize-6 {
		return nil, merrors.New(merrors.StageFEC, merrors.Bounds,
			"payload %d exceeds frame capacity %d", len(payload), FrameSize-6)
	}
	out := make([]byte, FrameSize)
	binary.BigEndian.PutUint32(out[0:4], timestamp)
	binary.BigEndian.PutUint16(out[4:6], uint16(len(payload)))
	copy(out[6:], payload)
	return out, nil
}

// Unframe reverses Frame.
func Unframe(b []byte) (uint32, []byte, error) {
	if len(b) != FrameSize {
		return 0, nil, merrors.At(merrors.StageFEC, merrors.Malformed, int64(len(b)),
			"frame must be %d bytes", FrameSize)
	}
	n := int(binary.BigEndian.Uint16(b[4:6]))
	if n > FrameSize-6 {
		return 0, nil, merrors.At(merrors.StageFEC, merrors.Malformed, 4,
			"payload length %d exceeds capacity", n)
	}
	payload := make([]byte, n)
	copy(payload, b[6:6+n])
	return binary.BigEndian.Uint32(b[0:4]), payload, nil
}

// Process runs the full pipeline. It returns a report even when non-fatal
// media problems were recorded; the error is reserved for failures that make
// delivery impossible (unsupported version or codec, broken container).
func Process(in Input) (*Report, error) {
	rep := &Report{}

	reg := adapter.NewRegistry()
	if err := reg.Register(adapter.NewMP4Adapter()); err != nil {
		return nil, err
	}
	plug, err := reg.Resolve(adapter.FormatMP4, adapter.MP4V1)
	if err != nil {
		return nil, err
	}
	parsed, err := plug.Parse(in.MP4)
	if err != nil {
		return nil, err
	}
	rep.Truncated = countTruncated(in.TruncatedMP4, plug)

	// Drift is estimated from Sender Reports. A rejected report never
	// touches the committed estimate.
	rep.DriftPPM = estimateDrift(in.SenderReports, videoRate(in))

	results := make(map[syncer.Kind]*trackResult, len(in.Tracks))
	for _, tr := range in.Tracks {
		res, err := processTrack(tr, in, rep)
		if err != nil {
			return nil, err
		}
		results[tr.Kind] = res
		rep.ClockWraps += res.wraps
		rep.UnrecoveredGaps += res.gaps
	}

	// Align both tracks on the shared clock.
	aligner, err := syncer.New(syncer.Config{
		MaxCorrectionTicks: in.MaxCorrectionTicks,
		ReferenceRate:      in.ReferenceRate,
	})
	if err != nil {
		return nil, err
	}
	for _, tr := range in.Tracks {
		if err := aligner.AddTrack(syncer.Track{
			Kind: tr.Kind, SSRC: tr.SSRC, ClockRate: tr.ClockRate, OffsetTicks: tr.OffsetTicks,
		}); err != nil {
			return nil, err
		}
	}

	// Build the delivered sample set, with MP4 metadata as the reference. A
	// delivered count above the expected count means duplicate payloads were
	// matched, which the reorder layer already reports as duplicates.
	var deliveredSamples []deliveredSample
	for _, tr := range in.Tracks {
		info, samples := parsed.Container.Tracks, parsed.Container.Samples[tr.MP4TrackID]
		idx := findTrackInfo(info, tr.MP4TrackID)
		if idx < 0 {
			return nil, merrors.New(merrors.StagePipeline, merrors.Invalid,
				"mp4 lacks track %d", tr.MP4TrackID)
		}
		res := results[tr.Kind]
		for _, p := range res.pkts {
			n := int(p.Seq - tr.StartSeq)
			if n < 0 || n >= len(samples) {
				continue
			}
			src := samples[n]
			// Roundtrip means the delivered payload is byte-identical to the
			// sample the container declares at that position. Recovered
			// packets count: parity restored the exact bytes.
			if len(p.Payload) == len(src.Data) && string(p.Payload) == string(src.Data) {
				rep.DeliveredSamples++
			}
			al, aerr := aligner.Align(syncer.Sample{
				Track: tr.Kind, SSRC: tr.SSRC, Extended: res.ext[p.Seq],
				Duration: src.Duration, Keyframe: src.IsKeyframe, Payload: p.Payload,
			})
			if aerr != nil {
				rep.OutOfSync++
				rep.Record(aerr)
			}
			deliveredSamples = append(deliveredSamples, deliveredSample{
				track: tr.Kind, trackID: tr.MP4TrackID, sample: src,
				aligned: al, recovered: res.recovered[p.Seq],
			})
		}
	}
	rep.ExpectedSamples = expectedSamples(parsed.Container, in.Tracks)
	if rep.ExpectedSamples > 0 {
		rep.RoundtripRate = float64(rep.DeliveredSamples) / float64(rep.ExpectedSamples)
	}

	// Keyframe index over the video track's per-sample durations.
	videoInfo, videoSamples := videoTrack(parsed.Container, in.Tracks)
	if videoInfo == nil {
		return nil, merrors.New(merrors.StagePipeline, merrors.Empty, "no video track in mp4")
	}
	durs := make([]uint64, len(videoSamples))
	keys := make([]bool, len(videoSamples))
	for i, s := range videoSamples {
		durs[i] = s.Duration
		keys[i] = s.IsKeyframe
	}
	idx, err := indexBuild(videoInfo.Timescale, durs, keys)
	if err != nil {
		return nil, err
	}
	rep.KeyframeHitRate = keyframeHitRate(idx, videoSamples)
	rep.TimelineEnd = idx.EndTick()

	cuts, err := idx.CutPoints(in.SegmentCount)
	if err != nil {
		return nil, err
	}
	rep.SegmentCount = len(cuts)
	rep.SegmentHashes = buildSegments(cuts, idx, deliveredSamples, rep)
	if err := verifySegments(rep.SegmentHashes); err != nil {
		return nil, err
	}

	// Re-mux the delivered samples through the adapter to prove the container
	// path end to end. The parsed metadata is the authority on timing.
	remux, err := remuxContainer(parsed.Container, deliveredSamples, in)
	if err != nil {
		rep.Record(err)
	} else {
		rep.RemuxBytes = len(remux)
	}

	// Stable ordering makes repeated runs produce identical reports.
	sort.SliceStable(rep.Errors, func(i, j int) bool { return rep.Errors[i].Error() < rep.Errors[j].Error() })
	return rep, nil
}
