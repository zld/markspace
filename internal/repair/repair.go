// Package repair turns raw demuxed blocks into a monotonic, track-aligned
// stream and rebuilds a random-access index from it.
//
// The repair stage never drops a block. Instead it records a Decision for
// every timestamp it had to change, so a recovered run can be audited: the
// summary hashes the decisions together with the index, and re-running from
// the beginning must reproduce both exactly.
package repair

import (
	"mediad/internal/clock"
	"mediad/internal/ebml"
	"mediad/internal/media"
	"mediad/internal/merr"
)

// DecisionKind classifies a timestamp repair.
type DecisionKind string

const (
	// DecisionUnwrap marks a block whose 32-bit counter had wrapped.
	DecisionUnwrap DecisionKind = "unwrap"
	// DecisionDrift marks a block adjusted by the track's rate estimate.
	DecisionDrift DecisionKind = "drift"
	// DecisionClamp marks a block nudged forward to preserve monotonicity.
	DecisionClamp DecisionKind = "clamp"
	// DecisionKeep marks a block that needed no change.
	DecisionKeep DecisionKind = "keep"
)

// Decision is one auditable timestamp repair record.
type Decision struct {
	Track    media.TrackID
	Sequence uint64
	Kind     DecisionKind
	RawNs    int64
	OutNs    int64
	DeltaNs  int64
}

// Options tunes the repair stage.
type Options struct {
	// Clock options passed to the timeline.
	Clock clock.Options
	// ReferenceTrack receives the shared clock. Default 1 when present.
	ReferenceTrack media.TrackID
	// MaxAVSkewNs is the audio/video deviation target used for reporting.
	MaxAVSkewNs int64
}

func (o Options) withDefaults() Options {
	if o.ReferenceTrack == 0 {
		o.ReferenceTrack = 1
	}
	if o.MaxAVSkewNs <= 0 {
		o.MaxAVSkewNs = 20 * 1e6
	}
	return o
}

// Result holds a repaired stream.
type Result struct {
	Blocks    []media.Block
	Decisions []Decision
	Timeline  *clock.Timeline
	// AVDeltasNs records the |video-audio| gap at each paired point.
	AVDeltasNs []int64
}

// Repair normalises the timestamps of blocks onto the shared clock.
//
// Blocks must be supplied in demux order. The pass is two-phase: every block
// is observed first so the clock can be calibrated over the whole clip, then
// every block is corrected. Nothing outside the block header is touched.
func Repair(blocks []ebml.Block, tracks []ebml.TrackInfo, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	ref := opts.ReferenceTrack
	if len(tracks) > 0 {
		ref = chooseReference(tracks, ref)
	}
	tl := clock.New(ref, opts.Clock)

	trackMeta := make(map[uint64]ebml.TrackInfo, len(tracks))
	for _, t := range tracks {
		trackMeta[t.Number] = t
	}

	// Observations in demux order: raw time plus the nominal cadence of the
	// owning track.
	rawNs := make([]int64, len(blocks))
	for i := range blocks {
		b := &blocks[i]
		meta := trackMeta[b.TrackNumber]
		nominal := int64(meta.DefaultDurNs)
		rawNs[i] = tl.Observe(clock.Observation{
			Track:     media.TrackID(b.TrackNumber),
			RawMs:     b.TimecodeMs,
			NominalNs: nominal,
		})
	}
	if err := tl.Calibrate(); err != nil {
		return nil, err
	}

	out := &Result{
		Blocks:    make([]media.Block, 0, len(blocks)),
		Decisions: make([]Decision, 0, len(blocks)),
		Timeline:  tl,
	}
	lastOut := make(map[media.TrackID]int64)
	haveOut := make(map[media.TrackID]bool)
	// Track the original 32-bit counter to detect a wrap between neighbours.
	prevRawMs := make(map[media.TrackID]uint32)
	havePrevRaw := make(map[media.TrackID]bool)

	for i := range blocks {
		b := blocks[i]
		tid := media.TrackID(b.TrackNumber)
		tt := media.TrackVideo
		if meta, ok := trackMeta[b.TrackNumber]; ok && meta.Type == 2 {
			tt = media.TrackAudio
		}
		corrected, err := tl.Corrected(tid, rawNs[i])
		if err != nil {
			return nil, err
		}
		kind := DecisionKeep
		wrapped := havePrevRaw[tid] && b.TimecodeMs < prevRawMs[tid] &&
			int64(prevRawMs[tid])-int64(b.TimecodeMs) > clock.WrapPeriodMs/2
		prevRawMs[tid] = b.TimecodeMs
		havePrevRaw[tid] = true

		switch {
		case wrapped:
			// The clock already unwrapped the counter; the decision records
			// the epoch jump for auditability.
			kind = DecisionUnwrap
		case haveOut[tid] && corrected <= lastOut[tid]:
			corrected = lastOut[tid] + 1
			kind = DecisionClamp
		case corrected != rawNs[i]:
			kind = DecisionDrift
		}
		// The timeline preserves monotonicity too; this second guard keeps the
		// invariant local to this stage so the stage stands alone.
		if haveOut[tid] && corrected <= lastOut[tid] {
			corrected = lastOut[tid] + 1
			kind = DecisionClamp
		}
		lastOut[tid] = corrected
		haveOut[tid] = true

		out.Blocks = append(out.Blocks, media.Block{
			Track:         tid,
			TrackType:     tt,
			Seq:           uint64(i),
			TimeNs:        corrected,
			RawNs:         rawNs[i],
			Keyframe:      b.Keyframe || tt == media.TrackAudio,
			Data:          b.Payload,
			ElementOffset: b.BlockOffset,
			EndOffset:     b.EndOffset,
			ClusterOffset: b.ClusterOffset,
		})
		out.Decisions = append(out.Decisions, Decision{
			Track:    tid,
			Sequence: uint64(i),
			Kind:     kind,
			RawNs:    rawNs[i],
			OutNs:    corrected,
			DeltaNs:  corrected - rawNs[i],
		})
	}
	out.AVDeltasNs = audioVideoDeltas(out.Blocks)
	return out, nil
}

// ValidateMonotonic returns an error if any track's timestamps are not
// strictly increasing in stream order.
func ValidateMonotonic(blocks []media.Block) error {
	last := make(map[media.TrackID]int64)
	seen := make(map[media.TrackID]bool)
	for i, b := range blocks {
		if seen[b.Track] && b.TimeNs <= last[b.Track] {
			return merr.New("repair.monotonic", merr.CodeInvalidIndex, int64(i),
				"track %d time %d not after %d", b.Track, b.TimeNs, last[b.Track])
		}
		last[b.Track] = b.TimeNs
		seen[b.Track] = true
	}
	return nil
}
