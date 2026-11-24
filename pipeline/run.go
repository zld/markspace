package pipeline

import (
	"context"
	"sort"
	"time"

	"mediacheck/audio"
	"mediacheck/color"
	"mediacheck/ebml"
	"mediacheck/index"
	"mediacheck/internal/fixture"
	"mediacheck/render"
	"mediacheck/rtp"
	"mediacheck/timeline"
)

// Run processes one seekable input. Corrupt input yields a populated Metrics
// plus a soft error; hard parameter/IO faults return an error directly.
func Run(ctx context.Context, in ReadSeekerAt, seed int64, cfg Config) (*Metrics, error) {
	cfg.Defaults()
	if ctx == nil {
		ctx = context.Background()
	}
	m := &Metrics{TruncatedAt: -1, TimelineTrusted: true, AudioLayoutValid: true}

	// 1. Input summary (size-bounded) and index binding.
	sum, err := index.Digest(in)
	if err != nil {
		return nil, err
	}
	m.InputSHA256 = sum.Hex()

	store, err := index.NewStore(cfg.IndexDir)
	if err != nil {
		return nil, err
	}
	if cached, reason := store.Load(sum); cached == nil {
		m.IndexRebuilt = true
		_ = reason
	}

	// 2. Fresh streaming scan; truncation retains the prior valid index.
	if _, err := in.Seek(0, 0); err != nil {
		return nil, timeline.New(timeline.CatIO, "rewind before scan failed").Wrapf(err)
	}
	scan, scanErr := ebml.Scan(in)
	m.Frames = countVideo(scan)
	m.CRCFailures = append([]int64(nil), scan.CheckFailures...)
	if scanErr != nil {
		m.TruncatedAt = scan.TruncatedAt
		m.Faults = append(m.Faults, "scan: "+scanErr.Error())
	}

	// Persist whatever projection the scan produced before the fault.
	rec := index.FromScan(sum, scan)
	if err := store.Save(sum, rec); err != nil {
		return nil, err
	}

	// 3. Locate tracks and normalise audio layout / colour metadata. Layout
	// and colour faults are recorded, not fatal: the corrupt-path report must
	// still carry frame, sync and recovery evidence.
	videoTrack, audioTrack := classify(scan)
	normaliseAudio(m, scan, audioTrack, cfg)
	normaliseColor(m, videoTrack, scan.ContainerColor, cfg)

	// 4. Merge the interleaved block timelines onto the common clock.
	blocks, tracks := toMergeInputs(scan)
	merged, err := timeline.Merge(blocks, tracks, timeline.DefaultMergeConfig())
	if err != nil {
		return nil, err
	}
	m.TimelineTrusted = merged.Trusted
	if !merged.Trusted {
		m.Faults = append(m.Faults, "timeline untrusted: "+merged.RejectReason)
	}

	// 5. Fixed-time frame export with deterministic SHA-256 hashes.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frameResult, err := exportFrames(scan, videoTrack, cfg)
	if err != nil {
		m.Faults = append(m.Faults, "frame export: "+err.Error())
		frameResult = &render.Result{}
	}
	m.FrameHashes = frameResult.HashSequence()
	for _, f := range frameResult.Frames {
		if f.Missing {
			m.Faults = append(m.Faults, "frame "+itoa(f.Index)+": "+f.Reason)
		}
	}

	// 6. RTP return-feed recovery with per-packet latency measurement.
	latencies := runRTP(ctx, m, cfg, seed)
	m.P95LatencyMs = percentileMs(latencies, 95)

	// 7. AV sync accounting across the recovered timeline.
	m.AVSyncErrorMs = computeSyncMs(merged)

	// 8. Publish only when every gate passes; temp output + completion marker.
	decidePublish(ctx, m, scan, cfg, videoTrack, audioTrack)
	return m, nil
}

func countVideo(scan *ebml.ScanResult) int {
	n := 0
	for _, b := range scan.Blocks {
		if b.TrackNumber == fixture.VideoTrackNumber {
			n++
		}
	}
	return n
}

func classify(scan *ebml.ScanResult) (video, audioT *ebml.TrackSpec) {
	for _, t := range scan.Tracks {
		switch {
		case t.IsVideo():
			video = t
		case t.IsAudio():
			audioT = t
		}
	}
	return video, audioT
}

func normaliseAudio(m *Metrics, scan *ebml.ScanResult, track *ebml.TrackSpec, cfg Config) {
	if track == nil {
		m.AudioLayoutValid = false
		m.Faults = append(m.Faults, "audio layout: no audio track in input")
		return
	}
	if _, err := audio.Resolve(track, cfg.AudioPolicy); err != nil {
		m.AudioLayoutValid = false
		m.Faults = append(m.Faults, "audio layout: "+err.Error())
	}
	// Sample accounting still runs on the declared rate so the report shows
	// input/output counts even when the layout itself is rejected.
	audioBlocks := 0
	for _, b := range scan.Blocks {
		if b.TrackNumber == track.Number {
			audioBlocks++
		}
	}
	if track.SampleRate == 0 {
		m.AudioLayoutValid = false
		m.Faults = append(m.Faults, "audio layout: zero sample rate")
		return
	}
	inSamples := int64(audioBlocks) * int64(track.SampleRate) / fpsOr(cfg.FPS)
	stats, err := audio.Plan(int64(track.SampleRate), cfg.TargetSampleRate, inSamples)
	if err != nil {
		m.Faults = append(m.Faults, "audio resample: "+err.Error())
		return
	}
	m.AudioSamplesIn = stats.SamplesIn
	m.AudioSamplesOut = stats.SamplesOut
	m.AudioDurationErrNs = stats.DurationErrorNs
}

func normaliseColor(m *Metrics, video *ebml.TrackSpec, container *ebml.ColorMeta, cfg Config) {
	var trackColor *ebml.ColorMeta
	if video != nil {
		trackColor = video.Color
	}
	rec, err := color.Resolve(trackColor, container, cfg.ColorPolicy)
	if err != nil {
		m.Faults = append(m.Faults, "color: "+err.Error())
		return
	}
	m.ColorSource = string(rec.Source)
	if ok, why := rec.SafeToConvert(); !ok {
		m.ColorUnsafeReason = why
		m.Faults = append(m.Faults, "color unsafe: "+why)
	}
}

func exportFrames(scan *ebml.ScanResult, video *ebml.TrackSpec, cfg Config) (*render.Result, error) {
	if video == nil {
		return nil, timeline.New(timeline.CatConfig, "no video track in input")
	}
	var blocks []*ebml.BlockInfo
	for _, b := range scan.Blocks {
		if b.TrackNumber == video.Number {
			blocks = append(blocks, b)
		}
	}
	return render.Export(render.VideoSource{
		TrackNumber:    video.Number,
		Width:          int(video.Width),
		Height:         int(video.Height),
		Blocks:         blocks,
		TruncatedAt:    scan.TruncatedAt,
		ExpectedFrames: cfg.Frames,
		FrameRate:      int64(cfg.FPS),
	}, cfg.Frames)
}

func runRTP(ctx context.Context, m *Metrics, cfg Config, seed int64) []float64 {
	feed := fixture.SimulateFeed(cfg.FeedConfig(seed))
	recv := rtp.NewReceiver(cfg.RTPWindow, cfg.RTPClockRate)
	latencies := make([]float64, 0, len(feed))
	for _, raw := range feed {
		if err := ctx.Err(); err != nil {
			break
		}
		start := time.Now()
		if err := recv.Handle(raw); err != nil {
			m.Faults = append(m.Faults, "rtp: "+err.Error())
			continue
		}
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000.0)
	}
	recv.Flush()
	st := recv.Stats()
	m.RTPLost, m.RTPRecovered = st.Lost, st.Recovered
	m.RTPUnrecoverable = st.Unrecoverable
	m.RecoveryRate = st.RecoveryRate()
	return latencies
}

// computeSyncMs reports the largest absolute AV timestamp difference at any
// matched frame boundary, in whole milliseconds.
func computeSyncMs(merged *timeline.MergeResult) int64 {
	video := merged.Tracks[fixture.VideoTrackNumber]
	audio := merged.Tracks[fixture.AudioTrackNumber]
	if video == nil || audio == nil {
		return 0
	}
	var worst int64
	vs := eventsByTime(video)
	as := eventsByTime(audio)
	j := 0
	for _, ve := range vs {
		for j+1 < len(as) && absDiff(as[j+1].TimeNs, ve.TimeNs) < absDiff(as[j].TimeNs, ve.TimeNs) {
			j++
		}
		if d := absDiff(as[j].TimeNs, ve.TimeNs); d > worst {
			worst = d
		}
	}
	return timeline.NsToMillis(worst)
}

func eventsByTime(tl *timeline.TrackTimeline) []*timeline.Event {
	ev := append([]*timeline.Event(nil), tl.Events...)
	sort.Slice(ev, func(i, j int) bool { return ev[i].TimeNs < ev[j].TimeNs })
	return ev
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

func fpsOr(fps int) int64 {
	if fps <= 0 {
		return 24
	}
	return int64(fps)
}
