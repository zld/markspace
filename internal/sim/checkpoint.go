package sim

import (
	"mediacore/adapter"
	"mediacore/audio"
	"mediacore/clock"
	"mediacore/pipeline"
)

// EngineSnapshot is the fully self-contained, serialisable checkpoint of an
// Engine. Every subsystem contributes its own checkpoint type so a resumed
// engine reproduces the uninterrupted run exactly.
type EngineSnapshot struct {
	Config           Config
	AudioStream      adapter.RTPStreamState
	VideoStream      adapter.RTPStreamState
	Drift            clock.DriftState
	Recovery         pipeline.RecoveryState
	Session          pipeline.SessionState
	Meter            audio.LoudnessState
	LostAudio        int64
	RecoveredAudio   int64
	BadPackets       int64
	DuplicatePackets int64
	LatePackets      int64
	VideoEvents      int64
	PCM              []int16
	Correction       int64
	AnchorSeq        int64
	AnchorRef        int64
	Anchored         bool
	ObservedOrigin   int64
	OriginSet        bool
}

// Checkpoint captures the engine's complete resumable state.
func (e *Engine) Checkpoint() (EngineSnapshot, error) {
	audioState, err := e.audioStream.Snapshot()
	if err != nil {
		return EngineSnapshot{}, err
	}
	videoState, err := e.videoStream.Snapshot()
	if err != nil {
		return EngineSnapshot{}, err
	}
	return EngineSnapshot{
		Config: e.cfg, AudioStream: audioState, VideoStream: videoState,
		Drift: e.drift.Snapshot(), Recovery: e.filler.Snapshot(e.videoRec),
		Session: e.session.Checkpoint(), Meter: e.meter.Snapshot(),
		LostAudio: e.lostAudio, RecoveredAudio: e.recoveredAudio,
		BadPackets: e.badPackets, DuplicatePackets: e.duplicatePackets,
		LatePackets: e.latePackets, VideoEvents: e.videoEvents, PCM: append([]int16(nil), e.pcm...),
		Correction: e.correction, AnchorSeq: e.anchorSeq, AnchorRef: e.anchorRef,
		Anchored: e.anchored, ObservedOrigin: e.observedOrigin, OriginSet: e.originSet,
	}, nil
}

// RestoreEngine rebuilds an engine from a checkpoint. The returned engine is
// ready to accept the remaining packets of the stream.
func RestoreEngine(snap EngineSnapshot) (*Engine, error) {
	audioStream, err := adapter.RestoreRTPStream(snap.AudioStream)
	if err != nil {
		return nil, err
	}
	videoStream, err := adapter.RestoreRTPStream(snap.VideoStream)
	if err != nil {
		return nil, err
	}
	filler, videoRec := pipeline.RestoreRecovery(1, AudioClockRate,
		AudioSamplesPerPkt, snap.Recovery)
	return &Engine{
		cfg:         snap.Config,
		audioStream: audioStream, videoStream: videoStream,
		drift:  clock.RestoreDrift(snap.Drift),
		filler: filler, videoRec: videoRec,
		session:   pipeline.RestoreSession(snap.Session),
		meter:     audio.RestoreLoudness(snap.Meter),
		lostAudio: snap.LostAudio, recoveredAudio: snap.RecoveredAudio,
		badPackets: snap.BadPackets, duplicatePackets: snap.DuplicatePackets,
		latePackets: snap.LatePackets, videoEvents: snap.VideoEvents, pcm: append([]int16(nil), snap.PCM...),
		correction: snap.Correction,
		anchorSeq:  snap.AnchorSeq, anchorRef: snap.AnchorRef, anchored: snap.Anchored,
		observedOrigin: snap.ObservedOrigin, originSet: snap.OriginSet,
	}, nil
}
