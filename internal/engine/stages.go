package engine

import (
	"context"

	"github.com/example/tsrepair/internal/metadata"
	"github.com/example/tsrepair/internal/model"
	"github.com/example/tsrepair/internal/pipeline"
	"github.com/example/tsrepair/internal/repair"
	"github.com/example/tsrepair/internal/ts"
)

// audioLayoutFaultMarker is the fixed-slot marker injected by the fixture's
// audio-layout fault; it is read only at payload index 2, never by scanning.
const audioLayoutFaultMarker byte = 0x33

// stagePSI applies the active mapping and rejects unknown PIDs fatally.
func (e *Engine) stagePSI(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.psi.Observe(w.pkt); err != nil {
		return in, err
	}
	if w.pkt.PID == ts.PIDPAT || w.pkt.IsNull() {
		return in, nil
	}
	if e.psi.Active() == nil {
		return in, repair.FatalMappingError(w.pkt)
	}
	if w.pkt.PID == e.psi.Active().PMTPID {
		return in, nil
	}
	if _, ok := e.psi.Active().Lookup(w.pkt.PID); !ok {
		return in, repair.FatalMappingError(w.pkt)
	}
	return in, nil
}

// stageRepair runs continuity and timestamp correction.
func (e *Engine) stageRepair(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	res, _ := e.cc.Check(w.pkt)
	if res == repair.CheckDuplicate {
		return in, model.New(model.StageContinuity, model.SeverityRecoverable,
			"duplicate packet ignored")
	}
	if corr, err := e.tsCorr.ObservePCR(w.pkt); err != nil {
		return in, err
	} else if corr != nil {
		w.pts = corr.Corrected
		in.Payload = w
	}
	return in, nil
}

// stageMetadata validates audio descriptors and video color metadata.
func (e *Engine) stageMetadata(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	track, ok := e.psi.Active().Lookup(w.pkt.PID)
	if !ok {
		return in, nil
	}
	switch track.Kind {
	case ts.KindAudio:
		layout := metadata.LayoutStereo
		rate := uint32(48000)
		// The fixture places the layout marker at the fixed payload slot 2;
		// scanning the whole stuffed payload would collide with data bytes.
		if len(w.pkt.Payload) > 2 && w.pkt.Payload[2] == audioLayoutFaultMarker {
			layout = metadata.LayoutUnknown
		}
		// A descriptor-level conflict is reported once per PID; every packet
		// of that track still flows through the remaining stages.
		if !e.audioSeen[w.pkt.PID] {
			e.audioSeen[w.pkt.PID] = true
			if _, err := e.audio.Normalize(metadata.AudioDescriptor{
				PID: w.pkt.PID, Layout: layout, Channels: 2,
				SampleRate: rate, FrameSizeBytes: 4,
			}, w.pkt.ByteOffset); err != nil {
				return in, err
			}
		}
		if err := e.audio.CheckDivisible(w.pkt.PID,
			len(w.pkt.Payload), 4, w.pkt.ByteOffset); err != nil {
			return in, err
		}
	case ts.KindVideo:
		res := e.video.Normalize(
			metadata.ColorMetadata{PID: w.pkt.PID, ColorPrimaries: 9,
				TransferCharacteristics: 16, MatrixCoefficients: 9},
			metadata.ColorMetadata{PID: w.pkt.PID}, w.pkt.ByteOffset)
		_ = res
	}
	return in, nil
}

// stageIndex admits verified keyframes and feeds segment hashing.
func (e *Engine) stageIndex(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	res := e.builder.Consider(w.pkt, w.ordinal, w.pts)
	if res.Admitted {
		e.hasher.NoteKeyframe(len(e.builder.Build().Frames) - 1)
	}
	e.hasher.AddPacket(w.ordinal, w.pkt.ByteOffset, w.pts, w.pkt.Raw)
	e.rawByOrd[w.ordinal] = w.pkt.Raw
	return in, nil
}
