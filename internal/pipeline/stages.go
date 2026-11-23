package pipeline

import (
	"bytes"

	"mediaroute/internal/clock"
	"mediaroute/internal/codec"
	"mediaroute/internal/index"
	"mediaroute/internal/timeline"
	"mediaroute/internal/transport"
)

// trackCodec maps the fixture SSRCs to their codec tags.
func trackCodec(ssrc uint32) codec.Codec {
	switch ssrc {
	case 10:
		return codec.H264
	case 20:
		return codec.H265
	case 30:
		return codec.AV1
	default:
		return codec.Codec("unknown")
	}
}

// runCodecAndIndex inspects every reassembled frame, commits complete frames
// through the journal in SegmentCount atomic stages and validates the
// keyframe index against only complete keyframe samples.
//
// Track inputs are assembled in arrival order (truncated and complete frames
// interleaved exactly as the transport stage emitted them) so the timeline
// clock advances monotonically per track; the per-frame sequence that the
// journal commits is tracked separately by pointer identity.
func (r *Runner) runCodecAndIndex(streamFrames map[uint32][]*transport.Frame) (
	[]*timeline.Input, int, int, int, error) {
	var inputs []*timeline.Input
	keyTotal := 0
	for _, frames := range r.fx.Frames {
		for _, fr := range frames {
			if fr.Keyframe {
				keyTotal++
			}
		}
	}
	// First pass: normalize truncated frames and gather the complete ones.
	var complete []collectedFrame
	truncated := 0
	corruptionApplied := false
	for _, ssrc := range sortedSSRCs(streamFrames) {
		c := trackCodec(ssrc)
		if c == codec.Codec("unknown") {
			return nil, 0, 0, 0, clock.E(clock.PhaseCodec, ssrc, -1, false,
				"unknown codec marker is unsupported", nil)
		}
		tc := timeline.NewTrackClock(ssrc, c)
		in := &timeline.Input{Track: tc, Codec: c, Keyframe: keyframeChecker(c)}
		segByFrame := map[*transport.Frame]string{}
		in.SegmentIDs = make([]string, 0, len(streamFrames[ssrc]))
		in.Samples = make([]uint32, 0, len(streamFrames[ssrc]))
		for _, fr := range streamFrames[ssrc] {
			in.Frames = append(in.Frames, fr)
			in.SegmentIDs = append(in.SegmentIDs, "")
			// The transport stage emits exactly one frame per source sample
			// in order, so the per-track ordinal is the sample number that
			// the keyframe index refers to.
			in.Samples = append(in.Samples, uint32(len(in.Samples)+1))
			if !fr.Complete {
				truncated++
				fr.Bytes = safeTruncate(c, fr.Bytes)
				continue
			}
			// Optional input corruption: damage a keyframe's structural
			// boundary so the codec stage fails loudly (never silently
			// repaired) instead of producing a keyframe or timeline hit.
			if r.opts.CorruptStream && !corruptionApplied {
				isKey, keyErr := inspectFrame(c, fr.Bytes)
				if keyErr != nil {
					return nil, 0, 0, 0, keyErr
				}
				if isKey {
					fr.Bytes = corruptStreamBytes(c, fr.Bytes)
					if _, err := inspectFrame(c, fr.Bytes); err != nil {
						return nil, 0, 0, 0, err
					}
					corruptionApplied = true
				}
			}
			complete = append(complete, collectedFrame{
				ssrc: ssrc, c: c, fr: fr, segByFrame: segByFrame})
		}
		inputs = append(inputs, in)
	}
	// Assign each complete frame to one of SegmentCount segments and commit
	// one segment at a time, then wire the segment ids back onto track inputs.
	segMap := map[uint32]map[*transport.Frame]string{}
	for _, cf := range complete {
		segMap[cf.ssrc] = cf.segByFrame
	}
	if err := r.commitSegments(complete); err != nil {
		return nil, 0, 0, 0, err
	}
	for _, in := range inputs {
		byFrame := segMap[in.Track.SSRC]
		for i, fr := range in.Frames {
			if id, ok := byFrame[fr]; ok {
				in.SegmentIDs[i] = id
			}
		}
	}
	// Validate each track's keyframe index against complete keyframe samples.
	keyHits := 0
	for _, ssrc := range sortedSSRCs(streamFrames) {
		hits, err := r.validateTrackIndex(ssrc, trackCodec(ssrc))
		if err != nil {
			return nil, 0, 0, 0, err
		}
		keyHits += hits
	}
	return inputs, keyTotal, keyHits, truncated, nil
}

// validateTrackIndex writes the kidx MP4 index via the journal and validates
// it against complete keyframe samples; corrupted indexes fail the stage.
func (r *Runner) validateTrackIndex(ssrc uint32, c codec.Codec) (int, error) {
	completeKeys := r.fx.KeySamples[ssrc]
	entries := make([]index.Entry, 0, len(completeKeys))
	for sample := range completeKeys {
		entries = append(entries, index.NewEntry(sample))
	}
	// Deterministic ascending order.
	sortEntries(entries)
	if r.opts.CorruptIndex && ssrc == 10 {
		entries[0].CRC ^= 0xFF // damage without repairing
	}
	var data []byte
	sw := newSeekBuffer(&data)
	if err := index.WriteMP4(sw, entries); err != nil {
		return 0, err
	}
	data = sw.Bytes() // pick up any growth during backward-seek header rewrite
	// Index commit is its own atomic journal stage; this is the crash point
	// named in the acceptance command.
	if err := r.coord.CommitStage(indexStage(ssrc),
		map[string][]byte{"kidx.mp4": data}); err != nil {
		return 0, err
	}
	got, err := index.ReadMP4(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	if err := index.Validate(c, ssrc, got, completeKeys); err != nil {
		return 0, err
	}
	return len(got), nil
}

func (r *Runner) fillMetrics(keyTotal, keyHits int) {
	m := &r.metrics
	m.KeyframeTotal = keyTotal
	m.KeyframeHits = keyHits
	m.Segments = len(r.manifest.Segments)
	if m.TruthPackets > 0 {
		// Distinct packets that survived the link over distinct packets sent.
		m.RoundtripRate = float64(m.Accepted) / float64(m.TruthPackets)
	}
	if keyTotal > 0 {
		m.KeyframeHitRate = float64(keyHits) / float64(keyTotal)
	}
}
