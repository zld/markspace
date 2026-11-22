package sim

import (
	"crypto/sha256"
	"encoding/binary"

	"mediacore/adapter"
	"mediacore/audio"
	"mediacore/clock"
	"mediacore/media"
	"mediacore/pipeline"
)

// senderRefs carries the active run's expanded-seq -> reference PTS table,
// used once to anchor the ideal timeline on the first emitted audio event.
var senderRefs map[int64]int64

// Run drives one synthetic stream through the complete kernel in a single
// uninterrupted pass and returns the metrics report.
func Run(cfg Config) (Report, error) {
	return runEngine(cfg, 0)
}

// RunWithCheckpoint processes the stream in two passes. It checkpoints after
// checkpointAt packets, restores the state onto a fresh engine, then finishes
// the remaining packets. The returned report must match the uninterrupted run;
// the checkpoint path count is set to one for the acceptance path report.
func RunWithCheckpoint(cfg Config, checkpointAt int) (Report, error) {
	if err := validate(cfg); err != nil {
		return Report{Status: statusRejected, Paths: map[string]int{}}, err
	}
	sender := NewSender(cfg)
	pkts := sender.Generate()
	senderRefs = sender.RefByExpandedSeq

	first := NewEngine(cfg)
	for i, rp := range pkts {
		if i == checkpointAt {
			break
		}
		first.ProcessPacket(rp)
	}
	snap, err := first.Checkpoint()
	if err != nil {
		return Report{}, err
	}
	restored, err := RestoreEngine(snap)
	if err != nil {
		return Report{}, err
	}
	for i := checkpointAt; i < len(pkts); i++ {
		restored.ProcessPacket(pkts[i])
	}
	restored.Flush()
	rep := restored.Finish()
	rep.Paths["checkpoints"] = 1
	return rep, nil
}

// runEngine is the shared uninterrupted implementation; checkpointCount is
// reserved for callers that want to annotate the checkpoint path.
func runEngine(cfg Config, checkpointCount int) (Report, error) {
	if err := validate(cfg); err != nil {
		return Report{Status: statusRejected, Paths: map[string]int{}}, err
	}
	sender := NewSender(cfg)
	pkts := sender.Generate()
	senderRefs = sender.RefByExpandedSeq

	e := NewEngine(cfg)
	for _, rp := range pkts {
		e.ProcessPacket(rp)
	}
	e.Flush()
	rep := e.Finish()
	rep.Paths["checkpoints"] = checkpointCount
	return rep, nil
}

func validate(cfg Config) error {
	if cfg.DurationS <= 0 || cfg.DurationS > 86400 {
		return media.NewError(media.CodeLimitExceeded, 0, nil, -1, "duration out of (0,24h]")
	}
	if cfg.LossBurst < 0 || cfg.LossBurst > 3 {
		return media.NewError(media.CodeLimitExceeded, 0, nil, -1, "loss-burst must be in [0,3]")
	}
	if cfg.ChunkMS <= 0 {
		return media.NewError(media.CodeMalformed, 0, nil, -1, "chunk-ms must be positive")
	}
	return nil
}

func nanosToMillis(v int64) float64 {
	return float64(v) / 1_000_000.0
}

func emitAudio(evs []adapter.RTPEvent, session *pipeline.Session, drift *clock.DriftEstimator,
	correction *int64, pcm *[]int16, meter *audio.LoudnessMeter,
	idealPTS func(int64) int64, anchored *bool, anchorSeq *int64, anchorRef *int64,
	normalizeObserved func(int64) int64) {
	refMap := senderRefs
	for _, ev := range evs {
		if !*anchored {
			if r, ok := refMap[ev.SeqExpanded]; ok {
				*anchored = true
				*anchorSeq = ev.SeqExpanded
				*anchorRef = r
			} else {
				// fallback: anchor to a zero reference at the first event
				*anchored = true
				*anchorSeq = ev.SeqExpanded
				*anchorRef = 0
			}
		}
		ref := idealPTS(ev.SeqExpanded)
		observed := normalizeObserved(ev.PTSNanos)
		// Drift estimation pairs the ideal reference time (x) with the
		// origin-normalised observed clock (y); slope converges to true ppm.
		drift.Observe(ref, observed, false)
		// Soft servo: the pre-correction offset is the measurable A/V sync
		// error. Each correction step is clamped to 2 ms, so a fast drift or a
		// jump leaves a non-zero residual that cannot be hidden in one packet.
		syncErr := abs64(observed - (ref + *correction))
		residual := observed - (ref + *correction)
		*correction += clock.Clamp(residual)
		latency := int64(10_000_000) + int64(ev.SeqExpanded*13%20)*1_000_000
		_ = session.Submit(pipeline.Event{
			TrackID: 1, Kind: pipeline.EventAudio, Seq: ev.SeqExpanded,
			PTSNanos: ref, DurationNanos: 10_000_000, LatencyNanos: latency,
			SyncRefNanos: ref + syncErr, HasSyncRef: true,
		})
		// decode deterministic PCM from the packet payload (mono PCM16)
		for k := 0; k+1 < len(ev.Payload); k += 2 {
			*pcm = append(*pcm, int16(binary.LittleEndian.Uint16(ev.Payload[k:k+2])))
		}
	}
}

func emitVideo(evs []adapter.RTPEvent, vr *pipeline.VideoRecovery, session *pipeline.Session) {
	for _, ev := range evs {
		isKey := ev.Marker
		when := media.Interval{Start: ev.PTSNanos, End: ev.PTSNanos + 33_333_333}
		ok, recEvs := vr.AcceptFrame(isKey, ev.SeqExpanded, when)
		for i := range recEvs {
			_ = session.Submit(recoveryEventToSession(2, recEvs[i]))
		}
		if ok {
			_ = session.Submit(pipeline.Event{
				TrackID: 2, Kind: pipeline.EventVideo, Seq: ev.SeqExpanded,
				PTSNanos: ev.PTSNanos, DurationNanos: 33_333_333,
				LatencyNanos: int64(12_000_000) + int64(ev.SeqExpanded*17%25)*1_000_000,
			})
		}
	}
}

func recoveryEvent(fe *pipeline.RecoveryEvent, recovered *int64, session *pipeline.Session,
	pcm *[]int16, meter *audio.LoudnessMeter) {
	if fe.Kind == pipeline.RecSilenceFill {
		*recovered++
		session.CreditRecovery()
		_ = session.Submit(pipeline.Event{
			TrackID: 1, Kind: pipeline.EventRecovery, Seq: fe.SeqStart,
			PTSNanos: fe.When.Start, DurationNanos: fe.When.Duration(),
			LatencyNanos: 8_000_000,
		})
		for k := 0; k < fe.Samples; k++ {
			*pcm = append(*pcm, 0)
		}
	}
}

func recoveryEventToSession(track uint32, re pipeline.RecoveryEvent) pipeline.Event {
	return pipeline.Event{
		TrackID: track, Kind: pipeline.EventRecovery, Seq: re.SeqStart,
		PTSNanos: re.When.Start, DurationNanos: re.When.Duration(),
		LatencyNanos: 8_000_000,
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// hashPCM16 hashes PCM16 samples through their fixed little-endian encoding so
// the digest is independent of struct layout and stable across runs.
func hashPCM16(pcm []int16) [32]byte {
	buf := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	return sha256.Sum256(buf)
}
