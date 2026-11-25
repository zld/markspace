package pipeline

import (
	"os"
	"path/filepath"

	"mediavault/internal/fixture"
	"mediavault/internal/ingest"
	"mediavault/internal/repair"
	"mediavault/internal/report"
	"mediavault/internal/timeline"
)

// rtpSpec is the deterministic RTP side-channel captured alongside the MP4
// fixture. Its timestamp counter wraps once so the unwrap path is exercised
// end-to-end; the boundary-jump fault additionally resets the clock.
func rtpSpec() fixture.RTPSpec {
	return fixture.RTPSpec{
		SSRC:          0x5151,
		ClockRate:     90000,
		PacketCount:   10,
		TimestampStep: 3000,
		PayloadSize:   24,
		Key:           21,
		Wrap:          true,
		BoundaryJump:  false,
		JumpIndex:     0,
	}
}

// ingestRTP writes and parses the RTP capture, linearizing it through a
// normalizer registered with the repairer. A wrap is transparent; a
// boundary jump is corrected with a traceable offset. The returned samples
// join the MP4 samples as an additional archive-backed track.
func (e *Engine) ingestRTP(rep *repair.BoundaryRepairer, b *report.Builder) ([]timeline.Sample, []*timeline.StageError) {
	dir := filepath.Join(e.cfg.WorkDir, "input")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.AddError(report.ErrorView{Stage: timeline.StageIngest, Kind: timeline.KindTruncatedCapture, Recoverable: true, Detail: err.Error()})
		return nil, nil
	}
	path := filepath.Join(dir, "sidechannel.rtp")
	spec := rtpSpec()
	if err := fixture.WriteRTP(path, spec); err != nil {
		b.AddError(report.ErrorView{Stage: timeline.StageIngest, Kind: timeline.KindTruncatedCapture, Recoverable: true, Detail: err.Error()})
		return nil, nil
	}
	capInfo := ingest.RTPCapture{
		Source:    "rtp",
		ClockRate: spec.ClockRate,
		Kind:      timeline.KindVideo,
		// Above the 2^32 wrap span so a legitimate counter wrap is unwrapped
		// transparently rather than mistaken for a source-switch jump.
		JumpThreshold: 1 << 33,
	}
	refs, err := ingest.ReadRTPFile(path, capInfo)
	if err != nil {
		if se, ok := err.(*timeline.StageError); ok {
			b.AddError(ev(se))
		}
		return nil, nil
	}
	samples, errs, err := ingest.IngestRTP(refs, capInfo)
	if err != nil {
		if se, ok := err.(*timeline.StageError); ok {
			b.AddError(ev(se))
		}
		return samples, nil
	}
	// Register the underlying normalizer so any reported jump can be
	// repaired through the shared, traceable repair path.
	id := timeline.TrackID{Source: "rtp", Index: 0, Kind: timeline.KindVideo}
	norm := timeline.NewTrackNormalizer(id, timeline.ClockRef{Source: "rtp", Clock: spec.SSRC}, spec.ClockRate, capInfo.JumpThreshold)
	rep.Register(id, norm)
	for _, se := range errs {
		if se.Kind == timeline.KindBoundaryJump {
			// The jumped raw clock is reconstructed from the packet order;
			// the repairer shifts it just past the last good timestamp.
			jumpedRaw := int64(refs[se.Position-1].Packet.Timestamp)
			if _, rerr := rep.Handle(se, jumpedRaw); rerr != nil {
				b.AddError(report.ErrorView{Stage: se.Stage, Kind: se.Kind, Offset: se.Offset, Position: se.Position, Track: se.Track.String(), Recoverable: false, Detail: rerr.Error()})
				continue
			}
		}
		b.AddError(ev(se))
	}
	return samples, nil
}

func ev(se *timeline.StageError) report.ErrorView {
	return report.ErrorView{
		Stage: se.Stage, Kind: se.Kind, Offset: se.Offset,
		Position: se.Position, Track: se.Track.String(),
		Recoverable: se.Recoverable, Detail: se.Detail,
	}
}
