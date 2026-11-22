package pipeline

import (
	"time"

	"github.com/pion/rtp"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/media"
	"github.com/archive/mediakernel/internal/rtprecover"
	"github.com/archive/mediakernel/internal/timeline"
)

// Input is one fully self-contained recovery job: the material bytes plus the
// asset description. Keeping it in-memory lets the demo generate everything
// locally with no network or external sample dependency.
type Input struct {
	Asset     *manifest.Asset
	MP4       []byte
	RTPPackets [][]byte // marshalled RTP packets, possibly damaged/ordered
	Estimator *rtprecover.Estimator
}

// Run executes the full data flow for one input and returns the result
// manifest and quality statistics. It is deterministic: identical inputs and
// configs produce identical output, which the resume and repeat-run paths rely
// on.
func Run(in *Input, cfg Config) (*manifest.ResultManifest, *Stats, error) {
	if in == nil || in.Asset == nil {
		return nil, nil, timeline.NewError(timeline.KindInvalidInput, "pipeline input missing")
	}
	if err := cfg.Normalize(); err != nil {
		return nil, nil, err
	}

	// ---- Stage 1: input digest + index (with invalidation and rebuild) ----
	digest := media.DigestBytes(in.MP4)
	stored, rebuilt, err := loadIndex(cfg.WorkDir, digest)
	if err != nil {
		return nil, nil, err
	}
	var idx *media.Index
	if rebuilt {
		idx, err = media.ScanBytes(in.MP4)
		if err != nil {
			return nil, nil, err
		}
		if err := saveIndex(cfg.WorkDir, &IndexRecord{InputDigest: digest, Index: idx}); err != nil {
			return nil, nil, err
		}
	} else {
		idx = stored.Index
	}
	if cfg.CorruptIndex {
		// Simulate on-disk index damage discovered after load: the stored
		// envelope no longer matches the input, so invalidate and rebuild once.
		rebuilt = true
		idx, err = media.ScanBytes(in.MP4)
		if err != nil {
			return nil, nil, err
		}
		if err := saveIndex(cfg.WorkDir, &IndexRecord{InputDigest: digest, Index: idx}); err != nil {
			return nil, nil, err
		}
	}

	// ---- Stage 2: unified timeline axis ----
	axis, err := buildAxis(in.Asset, idx)
	if err != nil {
		return nil, nil, err
	}

	// ---- Stage 3: streaming RTP recovery ----
	recovered, rtpStats, gaps, err := recoverStream(in, cfg, axis)
	if err != nil {
		return nil, nil, err
	}

	// ---- Stage 4: optional resume from the last completed segment ----
	var cp *manifest.Checkpoint
	if cfg.Resume {
		cp, err = loadCheckpoint(cfg.WorkDir)
		if err != nil {
			return nil, nil, err
		}
	}

	// ---- Stage 5: segmentation, silence and fingerprints ----
	result, stats, err := analyze(recovered, gaps, idx, axis, in, cfg, digest, rebuilt, cp)
	if err != nil {
		return nil, nil, err
	}
	stats.fillTransport(rtpStats)
	return result, stats, nil
}

// buildAxis converts the asset's non-zero start offset and any edit list onto
// the single timeline axis.
func buildAxis(asset *manifest.Asset, idx *media.Index) (*timeline.Axis, error) {
	startOffset := timeline.Time(asset.StartOffsetUS)
	if startOffset == 0 && len(idx.Tracks) > 0 {
		startOffset = timeline.Time(idx.FirstMediaUS)
	}
	axis := timeline.NewAxis(startOffset)
	if len(asset.Edits) == 0 {
		// Presented end is the end of the longest track.
		var mediaEnd timeline.Time
		for _, tr := range idx.Tracks {
			if t := timeline.Time(tr.MediaEndUS); t > mediaEnd {
				mediaEnd = t
			}
		}
		if err := axis.IdentityEdit(mediaEnd); err != nil {
			return nil, err
		}
		return axis, nil
	}
	for _, e := range asset.Edits {
		if err := axis.AddEdit(timeline.Edit{
			MediaStart: timeline.Time(e.MediaStartUS),
			Start:      timeline.Time(e.StartUS),
			Duration:   timeline.Time(e.DurationUS),
		}); err != nil {
			return nil, err
		}
	}
	return axis, nil
}

// recoveredFrame is one ordered packet translated onto the unified axis with
// synthetic PCM derived deterministically from its payload (the fixtures carry
// no codec, so PCM is derived, not decoded).
type recoveredFrame struct {
	seq       uint16
	startUS   int64
	endUS     int64
	pcm       []byte
	rawBytes  int
}

// recoverStream drives the reorder window and burst detector and projects
// packet timestamps onto the unified axis.
func recoverStream(in *Input, cfg Config, axis *timeline.Axis) ([]recoveredFrame, rtprecover.Stats, []manifest.LossGap, error) {
	win, err := rtprecover.NewWindow(cfg.ReorderWindow)
	if err != nil {
		return nil, rtprecover.Stats{}, nil, err
	}
	det, err := rtprecover.NewDetector(cfg.LossThreshold)
	if err != nil {
		return nil, rtprecover.Stats{}, nil, err
	}
	var ordered []rtprecover.OrderedPacket
	// RTP absolute timestamps are arbitrary; anchor the first packet and map
	// deltas onto the axis. Kept as run-local state so repeat runs are stable.
	baseSet := false
	var baseTS uint32
	feed := func(raw []byte) error {
		var p rtp.Packet
		if err := p.Unmarshal(raw); err != nil {
			return timeline.WrapError(timeline.KindCorrupt, "rtp unmarshal failed", err)
		}
		if !baseSet {
			baseTS = p.Timestamp
			baseSet = true
		}
		tlUS, err := rtpToAxis(p.Timestamp, baseTS, in.Asset.ClockRate, axis)
		if err != nil {
			return err
		}
		out, err := win.Push(&p, tlUS)
		if err != nil {
			return err
		}
		for _, op := range out {
			det.Observe(op)
			ordered = append(ordered, op)
		}
		if in.Estimator != nil {
			_ = in.Estimator.Observe(len(raw), packetInterval())
		}
		return nil
	}
	for _, raw := range in.RTPPackets {
		if err := feed(raw); err != nil {
			return nil, rtprecover.Stats{}, nil, err
		}
	}
	for _, op := range win.Flush() {
		det.Observe(op)
		ordered = append(ordered, op)
	}
	streamEnd := int64(axis.TimelineDuration())
	gaps := det.Finish(streamEnd)

	frames := make([]recoveredFrame, 0, len(ordered))
	stepUS, _ := timeline.FromScale(int64(in.Asset.ClockRate/50), in.Asset.ClockRate)
	for i, op := range ordered {
		pcm := payloadToPCM(op.Packet.Payload)
		start := op.TimelineUS
		end := start + int64(stepUS)
		if i == len(ordered)-1 {
			if end > streamEnd && streamEnd > start {
				end = streamEnd
			}
		}
		frames = append(frames, recoveredFrame{
			seq: op.Packet.SequenceNumber, startUS: start, endUS: end,
			pcm: pcm, rawBytes: len(op.Packet.Payload),
		})
	}
	return frames, win.Stats(), gaps, nil
}

// rtpToAxis converts an RTP timestamp to a unified-axis microsecond value.
// Absolute RTP timestamps are arbitrary, so only the signed modular delta from
// the anchored base is meaningful; the axis start offset is applied once.
func rtpToAxis(ts, baseTS uint32, clockRate uint32, axis *timeline.Axis) (int64, error) {
	delta := int64(int32(ts - baseTS)) // signed modular delta
	local, err := timeline.FromScale(delta, clockRate)
	if err != nil {
		return 0, err
	}
	t, err := timeline.Add(axis.StartOffset, local)
	if err != nil {
		return 0, err
	}
	return int64(t), nil
}

// packetInterval returns the nominal 20 ms packet spacing.
func packetInterval() time.Duration { return 20 * time.Millisecond }

// payloadToPCM expands arbitrary RTP payload bytes into deterministic signed
// 16-bit PCM samples for silence/fingerprint analysis.
func payloadToPCM(payload []byte) []byte {
	pcm := make([]byte, 2*len(payload))
	for i, b := range payload {
		// Map each byte to a signed 16-bit amplitude; zero bytes stay silent.
		v := int16(b) * 128
		pcm[2*i] = byte(v)
		pcm[2*i+1] = byte(v >> 8)
	}
	return pcm
}
