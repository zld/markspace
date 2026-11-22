package sim

import (
	"crypto/sha256"
	"encoding/hex"

	"mediacore/adapter"
	"mediacore/audio"
	"mediacore/clock"
	"mediacore/pipeline"
)

// Engine carries every piece of mutable kernel state for one run so it can be
// snapshotted mid-stream and resumed on a fresh engine with identical output.
type Engine struct {
	cfg Config

	audioStream *adapter.RTPStream
	videoStream *adapter.RTPStream
	drift       *clock.DriftEstimator
	filler      *pipeline.AudioFiller
	videoRec    *pipeline.VideoRecovery
	session     *pipeline.Session
	meter       *audio.LoudnessMeter

	lostAudio, recoveredAudio int64
	badPackets                int64
	duplicatePackets          int64
	latePackets               int64
	pcm                       []int16
	correction                int64

	anchorSeq, anchorRef int64
	anchored             bool
	observedOrigin       int64
	originSet            bool
}

// NewEngine builds a fresh engine for the given config.
func NewEngine(cfg Config) *Engine {
	audioStream, _ := adapter.NewRTPStream(AudioSSRC, AudioClockRate, reorderWindow)
	videoStream, _ := adapter.NewRTPStream(VideoSSRC, VideoClockRate, reorderWindow)
	return &Engine{
		cfg:         cfg,
		audioStream: audioStream, videoStream: videoStream,
		drift:    clock.NewDriftEstimator(256, 250_000_000),
		filler:   pipeline.NewAudioFiller(1, AudioClockRate, AudioSamplesPerPkt),
		videoRec: pipeline.NewVideoRecovery(2),
		session:  pipeline.NewSession(),
		meter:    audio.NewLoudnessMeter(2),
	}
}

func (e *Engine) idealPTS(expSeq int64) int64 {
	if !e.anchored {
		return 0
	}
	return e.anchorRef + (expSeq-e.anchorSeq)*10_000_000
}

func (e *Engine) normalizedObserved(rawPTS int64) int64 {
	if !e.originSet {
		e.observedOrigin, e.originSet = rawPTS, true
	}
	return rawPTS - e.observedOrigin
}

// ProcessPacket advances the engine by one generated packet.
func (e *Engine) ProcessPacket(rp RawPacket) {
	if rp.Data == nil {
		return
	}
	if rp.Corrupt {
		e.badPackets++
	}
	var res adapter.PushResult
	if rp.IsVideo {
		res = e.videoStream.Push(rp.Data)
	} else {
		res = e.audioStream.Push(rp.Data)
	}
	switch res.Kind {
	case adapter.RTPBad:
		e.badPackets++
	case adapter.RTPDuplicate:
		e.duplicatePackets++
	case adapter.RTPLate:
		e.latePackets++
	}
	if !rp.IsVideo {
		e.emitAudio(res.Events)
		for _, h := range e.audioStream.DrainHoles() {
			e.lostAudio += h.End - h.Start
			fills := e.filler.FillHole(h.Start, h.End, e.idealPTS)
			for i := range fills {
				e.recoveryEvent(&fills[i])
			}
		}
	} else {
		e.emitVideo(res.Events)
	}
}

// Flush drains both streams at end of input, reconciling final holes.
func (e *Engine) Flush() {
	evs, holes := e.audioStream.Flush()
	for _, h := range holes {
		e.lostAudio += h.End - h.Start
		fills := e.filler.FillHole(h.Start, h.End, e.idealPTS)
		for i := range fills {
			e.recoveryEvent(&fills[i])
		}
	}
	e.emitAudio(evs)
	vEvts, vHoles := e.videoStream.Flush()
	for _, h := range vHoles {
		ev := e.videoRec.OpenHole(h.Start, h.End, h)
		_ = e.session.Submit(recoveryEventToSession(2, ev))
	}
	e.emitVideo(vEvts)
}

// Finish runs the audio normalisation/loudness tail and builds the report.
func (e *Engine) Finish() Report {
	norm, err := audio.Normalize(e.pcm, len(e.pcm), audio.Mono, audio.Stereo, audio.FillZero)
	if err != nil {
		return Report{Status: statusRejected}
	}
	e.meter.Accumulate(norm.Out)
	e.meter.ComputeGain()
	processed := e.meter.Apply(norm.Out)
	audioHash := hashPCM16(processed)
	e.session.CreditAudioFrames(1, int64(len(e.pcm)))

	m := e.session.Snapshot()
	chainDigest, _ := hex.DecodeString(m.EventSHA256)
	combined := sha256.Sum256(append(chainDigest, audioHash[:]...))

	rate := 1.0
	if e.lostAudio > 0 {
		rate = float64(e.recoveredAudio) / float64(e.lostAudio)
	}
	syncP95 := nanosToMillis(m.SyncErrorP95Nanos)
	latP95 := nanosToMillis(m.LatencyP95Nanos)

	rep := Report{
		SyncErrorMSP95: syncP95, PacketRecoveryRate: rate,
		PipelineLatencyMSP95: latP95, DriftPPMEstimate: e.drift.DriftPPM(),
		AudioSamples: int64(len(e.pcm)), EventSHA256: hex.EncodeToString(combined[:]),
		RecoveryCount: m.RecoveryCount,
		Paths: map[string]int{
			"normal_events":    int(m.EventCount),
			"corrupt_rejected": int(e.badPackets),
			"duplicates":       int(e.duplicatePackets),
			"late_rejected":    int(e.latePackets),
			"checkpoints":      0,
		},
	}
	rep.Status = statusOK
	if syncP95 > 20 || rate < 0.99 || latP95 > 50 {
		rep.Status = statusMetricsFailed
	}
	return rep
}

func (e *Engine) emitAudio(evs []adapter.RTPEvent) {
	emitAudio(evs, e.session, e.drift, &e.correction, &e.pcm, e.meter,
		e.idealPTS, &e.anchored, &e.anchorSeq, &e.anchorRef, e.normalizedObserved)
}

func (e *Engine) emitVideo(evs []adapter.RTPEvent) {
	emitVideo(evs, e.videoRec, e.session)
}

func (e *Engine) recoveryEvent(fe *pipeline.RecoveryEvent) {
	// recoveryEvent increments the recovered counter itself.
	recoveryEvent(fe, &e.recoveredAudio, e.session, &e.pcm, e.meter)
}
