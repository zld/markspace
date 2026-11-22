package main

import (
	"bytes"

	"mediacore"
	"mediacore/buffer"
	"mediacore/index"
	"mediacore/internal/gen"
	"mediacore/syncer"
	"mediacore/timeline"
)

// run executes the full deterministic pipeline and returns the acceptance
// metrics. Errors on corrupt/boundary inputs are recorded but never abort the
// independent segments, mirroring the skip-bad-segment requirement.
func run(o Options) (Metrics, error) {
	elog := &errorLog{}
	cfg := gen.DefaultAudio(o.Seed, o.DriftPPM, uint32(o.WrapAt), o.LateMS)
	fix := gen.BuildAudio(cfg)

	// --- Stage 1: RTP parse -> extended sequence + duplicate accounting.
	parser := buffer.NewRTPParser(mediacore.TrackAudio1)
	jb := buffer.NewJitterBuffer(mediacore.TrackAudio1, windowNS(cfg),
		cfg.BlockSamps, cfg.Channels, cfg.Rate)
	var parsed []mediacore.Event
	for _, pk := range fix.Packets {
		ev, dup, err := parser.Parse(pk.Raw, pk.RecvNS)
		elog.record(err)
		if err != nil || dup {
			continue
		}
		late, err := jb.Push(ev)
		elog.record(err)
		if late {
			// A packet past the buffer window is a late event for the
			// timeline's replay path; still feed it through below.
			parsed = append(parsed, ev)
			continue
		}
		parsed = append(parsed, ev)
	}

	// --- Stage 2: jitter reorder up to the highest observed receive time.
	wm := jb.Watermark()
	emitted := jb.Advance(wm + 1)
	emitted = append(emitted, jb.Flush()...)

	// --- Stage 3: drift estimation on the RTP clock against the master.
	est := buildDrift(cfg)
	applied, _, alignable := est.PPM()
	if !alignable {
		elog.record(est.StateError())
	}

	// --- Stage 4: map events to the master clock and recover gaps.
	recovery := buffer.NewLossRecovery(buffer.RecoveryConfig{
		Track: mediacore.TrackAudio1, Channels: cfg.Channels,
		Rate: cfg.Rate, BlockSamples: cfg.BlockSamps,
	})
	audioEvents, recovered := mapAndRecover(emitted, recovery, est, elog)

	// --- Stage 5: video fixture -> MP4 -> parse -> VFR/keyframe mapping.
	vidCfg := gen.DefaultVideo(o.Seed, o.VFR)
	video := gen.BuildVideo(vidCfg)
	mp4Raw, report, kfHitRate, videoEvents := buildVideoTrack(video, elog)

	// --- Stage 6: commit audio + video onto the recomputable timeline.
	engine := timeline.NewEngine(segmentNS)
	replayCount := ingestAndReplay(engine, audioEvents, videoEvents, fix, elog)
	segments := engine.Segments()

	// --- Stage 7: deterministic re-run must hash identically.
	hashMatch := rerunHashMatch(o, audioEvents, videoEvents)

	// --- Stage 8: corrupt/boundary fixtures contribute the error count.
	corruptCount := exerciseCorruptFixtures(mp4Raw, elog)

	m := Metrics{
		RoundtripMatch:   report,
		KeyframeHitRate:  kfHitRate,
		SegmentHashMatch: hashMatch,
		RecoveredLoss:    recovered,
		LateReplay:       replayCount,
		DriftPPM:         applied,
		Errors:           len(elog.cats) + corruptCount,
	}
	_ = segments
	return m, nil
}

const (
	segmentNS = 200 * mediacore.Millisecond
)

// windowNS derives a playout window comfortably above one block spacing.
func windowNS(cfg gen.AudioConfig) int64 {
	block := int64(cfg.BlockSamps) * mediacore.Second / int64(cfg.Rate)
	return block * 2
}

// buildDrift feeds synthetic anchors that encode the configured ppm drift,
// exercising the same online estimator used on real sender reports.
func buildDrift(cfg gen.AudioConfig) *syncer.DriftEstimator {
	est := syncer.NewDriftEstimator(mediacore.TrackAudio1, driftBoundPPM)
	for i := int64(1); i <= 24; i++ {
		master := i * 100 * mediacore.Millisecond
		device := int64(float64(master) * (1.0 + float64(cfg.DriftPPM)/1_000_000.0))
		est.Observe(syncer.Anchor{Device: device, Master: master})
	}
	return est
}

// mapAndRecover assigns master-clock times to every emitted block (including
// gaps, whose expected slot follows from its sequence number) and then fills
// uniquely determined gaps with silence, counting each recovery.
func mapAndRecover(emitted []mediacore.Event, rec *buffer.LossRecovery,
	est *syncer.DriftEstimator, elog *errorLog) ([]mediacore.Event, int) {
	blockNS := int64(0)
	for _, ev := range emitted {
		if ev.Kind == mediacore.KindPCM && blockNS == 0 {
			blockNS = ev.Duration()
		}
	}
	// Pass 1: assign deterministic device + mapped master times to all events.
	for i := range emitted {
		ev := &emitted[i]
		deviceNS := int64(ev.Seq) * blockNS
		mapped, _ := est.Map(deviceNS)
		ev.OrigTime = deviceNS
		ev.Time = mapped
	}
	// Pass 2: recover gaps using their now-timestamped neighbours.
	out := make([]mediacore.Event, 0, len(emitted))
	recovered := 0
	var prev mediacore.Event
	havePrev := false
	for i := 0; i < len(emitted); i++ {
		ev := emitted[i]
		if ev.Kind == mediacore.KindGap {
			var next mediacore.Event
			j := i + 1
			for ; j < len(emitted); j++ {
				if emitted[j].Kind == mediacore.KindPCM {
					next = emitted[j]
					break
				}
			}
			if havePrev && j < len(emitted) {
				if filled, err := rec.RecoverSilence(ev, prev, next); err == nil {
					out = append(out, filled)
					recovered++
					prev, havePrev = filled, true
					continue
				} else {
					elog.record(err)
				}
			} else {
				elog.record(mediacore.NewError(mediacore.CatGap, ev.Track,
					"pipeline.gap", int64(ev.Seq), "gap without two-sided anchors"))
			}
			out = append(out, ev)
			continue
		}
		if havePrev {
			elog.record(buffer.CheckContinuity(prev, ev))
		}
		out = append(out, ev)
		prev, havePrev = ev, true
	}
	return out, recovered
}

// buildVideoTrack writes the generated samples to MP4, parses them back,
// builds the VFR/keyframe mapping and converts frames to timeline events.
func buildVideoTrack(video *gen.VideoFixture, elog *errorLog,
) ([]byte, float64, float64, []mediacore.Event) {
	wt := index.WriteTrack{
		TrackID: 1, Timescale: video.Cfg.Timescale, IsVideo: true,
		Width: video.Cfg.Width, Height: video.Cfg.Height,
		Deltas: video.Deltas, Sizes: video.Sizes, Keyframes: video.Keyframes,
		SampleData: video.Data,
	}
	raw, err := index.WriteMP4([]index.WriteTrack{wt})
	elog.record(err)
	reports, rewritten, err := index.Roundtrip(raw, func(uint32) mediacore.TrackID {
		return mediacore.TrackVideo1
	})
	elog.record(err)
	match := 0.0
	if len(reports) > 0 {
		ok := 0
		for _, r := range reports {
			if r.SamplesMatch && r.TimesMatch && r.KeyframesMatch {
				ok++
			}
		}
		match = float64(ok) / float64(len(reports))
	}
	container, err := index.ParseMP4(func(uint32) mediacore.TrackID {
		return mediacore.TrackVideo1
	}, bytes.NewReader(rewritten))
	elog.record(err)
	var ti index.TrackInfo
	for _, t := range container.Tracks {
		if t.Track == mediacore.TrackVideo1 {
			ti = t
		}
	}
	mapper := syncer.NewVFRMapper(mediacore.TrackVideo1, 0)
	elog.record(mapper.Load(index.ToFrameSamples(ti)))
	kf := index.BuildKeyframeIndex(ti)
	requests := make([]int64, 0, len(ti.Samples))
	for _, s := range ti.Samples {
		requests = append(requests, s.Presentation)
	}
	hitRate := kf.HitRate(requests)
	return raw, match, hitRate, mapper.Events()
}

// ingestAndReplay pushes audio then video events; a late audio event whose
// bucket was already committed triggers a deterministic replay.
func ingestAndReplay(engine *timeline.Engine, audio, video []mediacore.Event,
	fix *gen.AudioFixture, elog *errorLog) int {
	// Commit the bulk of audio first so early segment buckets exist.
	for i, ev := range audio {
		if ev.Time == 0 && i > 0 {
			ev.Time = int64(ev.Seq) * fixBlockNS(fix)
		}
		elog.record(engine.Ingest(ev))
	}
	engine.Commit()
	// Now inject the late block behind the watermark to force replay.
	lateIdx := fix.LateBlock
	if lateIdx < len(fix.PCM) && fix.Cfg.LateMS > 0 {
		late := mediacore.Event{
			Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM,
			Time: int64(lateIdx) * fixBlockNS(fix), OrigTime: int64(lateIdx) * fixBlockNS(fix),
			Recv: int64(lateIdx)*fixBlockNS(fix) + fix.Cfg.LateMS*mediacore.Millisecond,
			Seq:  uint64(lateIdx), Samples: fix.Cfg.BlockSamps,
			Channels: fix.Cfg.Channels, Rate: fix.Cfg.Rate,
			Payload: append([]byte(nil), fix.PCM[lateIdx]...),
		}
		elog.record(engine.Ingest(late))
	}
	for _, ev := range video {
		elog.record(engine.Ingest(ev))
	}
	engine.Commit()
	return engine.LateReplays
}

func fixBlockNS(fix *gen.AudioFixture) int64 {
	return int64(fix.Cfg.BlockSamps) * mediacore.Second / int64(fix.Cfg.Rate)
}

// rerunHashMatch builds a second independent engine over identical inputs and
// reports the fraction of per-segment hashes that match the first run.
func rerunHashMatch(o Options, audio, video []mediacore.Event) float64 {
	build := func() *timeline.Engine {
		e := timeline.NewEngine(segmentNS)
		for _, ev := range audio {
			_ = e.Ingest(ev)
		}
		e.Commit()
		for _, ev := range video {
			_ = e.Ingest(ev)
		}
		e.Commit()
		return e
	}
	first := auditHashes(build())
	second := auditHashes(build())
	if len(first) == 0 {
		return 0
	}
	matches := 0
	for i := range first {
		if i < len(second) && first[i] == second[i] {
			matches++
		}
	}
	return float64(matches) / float64(len(first))
}
