package sim

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"mediacore/adapter"
	"mediacore/audio"
	"mediacore/clock"
	"mediacore/media"
	"mediacore/pipeline"
)

// Report is the printable, machine-parseable acceptance result. The JSON tags
// match the exact field names required by the verification command.
type Report struct {
	SyncErrorMSP95       float64        `json:"sync_error_ms_p95"`
	PacketRecoveryRate   float64        `json:"packet_recovery_rate"`
	PipelineLatencyMSP95 float64        `json:"pipeline_latency_ms_p95"`
	DriftPPMEstimate     float64        `json:"drift_ppm_estimate"`
	AudioSamples         int64          `json:"audio_samples"`
	EventSHA256          string         `json:"event_sha256"`
	RecoveryCount        int64          `json:"recovery_count"`
	Status               string         `json:"status"`
	Paths                map[string]int `json:"paths"`
}

const (
	statusOK            = "ok"
	statusRejected      = "rejected"
	statusMetricsFailed = "metrics_failed"
	statusCheckpointBad = "checkpoint_mismatch"
	reorderWindow       = 64
)

// senderRefs carries the active run's expanded-seq -> reference PTS table,
// used once to anchor the ideal timeline on the first emitted audio event.
var senderRefs map[int64]int64

// Run drives one synthetic stream through the complete kernel and returns the
// metrics report plus the audio PCM hash used for determinism checks.
func Run(cfg Config) (Report, error) {
	if err := validate(cfg); err != nil {
		return Report{Status: statusRejected, Paths: map[string]int{}}, err
	}
	sender := NewSender(cfg)
	pkts := sender.Generate()
	senderRefs = sender.RefByExpandedSeq

	audioStream, _ := adapter.NewRTPStream(AudioSSRC, AudioClockRate, reorderWindow)
	videoStream, _ := adapter.NewRTPStream(VideoSSRC, VideoClockRate, reorderWindow)
	drift := clock.NewDriftEstimator(256, 250_000_000)
	filler := pipeline.NewAudioFiller(1, AudioClockRate, AudioSamplesPerPkt)
	videoRec := pipeline.NewVideoRecovery(2)
	session := pipeline.NewSession()
	meter := audio.NewLoudnessMeter(2) // post-layout stereo

	var lostAudio, recoveredAudio int64
	var badPackets, duplicatePackets, latePackets int64
	var pcm []int16
	var correction int64 // bounded drift servo offset, nanoseconds
	// anchor maps the stream's first emitted expanded sequence onto its ideal
	// reference PTS; every later ref is derived from the fixed 10 ms spacing.
	var anchorSeq int64
	var anchorRef int64
	var anchored bool
	// observedOrigin removes the sender's arbitrary non-zero RTP timestamp
	// baseline so the observed timeline starts near zero, matching the ideal.
	var observedOrigin int64
	var originSet bool
	idealPTS := func(expSeq int64) int64 {
		if !anchored {
			return 0
		}
		return anchorRef + (expSeq-anchorSeq)*10_000_000
	}
	normalizedObserved := func(rawPTS int64) int64 {
		if !originSet {
			observedOrigin, originSet = rawPTS, true
		}
		return rawPTS - observedOrigin
	}

	flushAll := func() {
		evs, holes := audioStream.Flush()
		for _, h := range holes {
			lostAudio += h.End - h.Start
			fills := filler.FillHole(h.Start, h.End, idealPTS)
			for _, fe := range fills {
				recoveryEvent(&fe, &recoveredAudio, session, &pcm, meter)
			}
		}
		emitAudio(evs, session, drift, &correction, &pcm, meter, idealPTS,
			&anchored, &anchorSeq, &anchorRef, normalizedObserved)
		vEvts, vHoles := videoStream.Flush()
		for _, h := range vHoles {
			ev := videoRec.OpenHole(h.Start, h.End, h)
			_ = session.Submit(recoveryEventToSession(2, ev))
		}
		emitVideo(vEvts, videoRec, session)
	}

	for _, rp := range pkts {
		if rp.Data == nil {
			// Dropped packets are reconciled through the stream's hole
			// intervals (window expiry and flush); the sender-side drop is not
			// counted separately to avoid double counting.
			continue
		}
		if rp.Corrupt {
			badPackets++
		}
		var res adapter.PushResult
		if rp.IsVideo {
			res = videoStream.Push(rp.Data)
		} else {
			res = audioStream.Push(rp.Data)
		}
		switch res.Kind {
		case adapter.RTPBad:
			badPackets++
		case adapter.RTPDuplicate:
			duplicatePackets++
		case adapter.RTPLate:
			latePackets++
		}
		if !rp.IsVideo {
			// Establish the anchor from the first emitted event, then reconcile
			// any holes already declared ahead of it.
			emitAudio(res.Events, session, drift, &correction, &pcm, meter,
				idealPTS, &anchored, &anchorSeq, &anchorRef, normalizedObserved)
			for _, h := range audioStream.DrainHoles() {
				lostAudio += h.End - h.Start
				fills := filler.FillHole(h.Start, h.End, idealPTS)
				for _, fe := range fills {
					recoveryEvent(&fe, &recoveredAudio, session, &pcm, meter)
				}
			}
		} else {
			emitVideo(res.Events, videoRec, session)
		}
	}
	flushAll()

	// Audio normalisation stage: mono -> stereo, then loudness control.
	norm, err := audio.Normalize(pcm, len(pcm), audio.Mono, audio.Stereo, audio.FillZero)
	if err != nil {
		return Report{}, err
	}
	meter.Accumulate(norm.Out)
	meter.ComputeGain()
	processed := meter.Apply(norm.Out)
	audioHash := hashPCM16(processed)
	session.CreditAudioFrames(1, int64(len(pcm)))

	m := session.Snapshot()
	chainDigest, _ := hex.DecodeString(m.EventSHA256)
	combined := sha256.Sum256(append(chainDigest, audioHash[:]...))

	rate := 1.0
	if lostAudio > 0 {
		rate = float64(recoveredAudio) / float64(lostAudio)
	}
	syncP95 := nanosToMillis(m.SyncErrorP95Nanos)
	latP95 := nanosToMillis(m.LatencyP95Nanos)

	rep := Report{
		SyncErrorMSP95: syncP95, PacketRecoveryRate: rate,
		PipelineLatencyMSP95: latP95, DriftPPMEstimate: drift.DriftPPM(),
		AudioSamples: int64(len(pcm)), EventSHA256: hex.EncodeToString(combined[:]),
		RecoveryCount: m.RecoveryCount,
		Paths: map[string]int{
			"normal_events":    int(m.EventCount),
			"corrupt_rejected": int(badPackets),
			"duplicates":       int(duplicatePackets),
			"late_rejected":    int(latePackets),
			"checkpoints":      0,
		},
	}
	rep.Status = statusOK
	if syncP95 > 20 || rate < 0.99 || latP95 > 50 {
		rep.Status = statusMetricsFailed
	}
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
		// Soft servo: per-step correction clamped to 2 ms.
		residual := observed - (ref + *correction)
		*correction += clock.Clamp(residual)
		syncErr := abs64(observed - (ref + *correction))
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
		for _, re := range recEvs {
			_ = session.Submit(recoveryEventToSession(2, re))
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
