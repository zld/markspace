package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediacore/audio"
	"mediacore/cache"
	"mediacore/ingest"
	"mediacore/internal/sample"
	"mediacore/media"
	"mediacore/pipeline"
)

// runDemo executes the demo scenario: synthetic capture -> ingest ->
// audio normalization -> RTP over a lossy wire -> recovery -> cache ->
// checkpointed output, all deterministic in --seed.
func runDemo(f demoFlags) error {
	layoutName := map[int]string{1: "mono", 2: "stereo", 6: "5.1"}[f.channels]
	if layoutName == "" {
		return media.Fault(media.CodeLayoutUnknown, "demo", -1,
			"no declared layout for %d channels", f.channels)
	}
	targetLayout := audio.LayoutStereo
	if f.channels == 1 {
		targetLayout = audio.LayoutMono
	}
	video, idx := sample.H264(f.seed, 6, 5) // 30 frames, 1 s at 30 fps
	if f.corruptIndex {
		idx.Offsets[0]++ // declared index no longer matches the stream
	}
	full := true
	color := make([]ingest.ColorMeta, 6)
	for i := range color {
		color[i] = ingest.ColorMeta{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", FullRange: &full}
	}
	build := func(dir string, crashAfter int) (pipeline.Config, error) {
		store, err := pipeline.NewStore(filepath.Join(dir, "ckpt"))
		if err != nil {
			return pipeline.Config{}, err
		}
		return pipeline.Config{
			SourceID:     "capture:demo",
			Codec:        ingest.CodecH264,
			Video:        video,
			Index:        &idx,
			Audio:        sample.Audio(f.seed, f.rate, f.channels, 1000),
			AudioMeta:    ingest.AudioMeta{Rate: f.rate, Channels: f.channels, Layout: layoutName},
			Color:        color,
			TargetLayout: targetLayout,
			TargetRate:   f.targetRate,
			Wire:         sample.LossWire(f.seed, f.loss, f.burst),
			Store:        store,
			Cache:        cache.New(),
			OutPath:      filepath.Join(dir, "out.bin"),
			CrashAfter:   crashAfter,
		}, nil
	}
	exec := func(cfg pipeline.Config) (pipeline.Result, error) {
		r, err := pipeline.NewRunner(cfg)
		if err != nil {
			return pipeline.Result{}, err
		}
		return r.Run()
	}

	dirA, err := os.MkdirTemp("", "mediacore-demo")
	if err != nil {
		return media.Wrap(media.CodeInternal, "demo", -1, err, "mktemp")
	}
	defer os.RemoveAll(dirA)
	cfgA, err := build(dirA, 0)
	if err != nil {
		return err
	}
	res, err := exec(cfgA)
	if err != nil {
		return err
	}

	if res.ErrorCode != "" {
		fmt.Printf("error_code=%s\n", res.ErrorCode)
		fmt.Printf("recovered=%v\n", res.Recovered)
	}
	fmt.Printf("av_sync_ms=%d\n", res.AVSyncP95Ms)
	fmt.Printf("loss_recovery_pct=%.1f\n", res.LossRecoveryPct)
	fmt.Printf("latency_p95_ms=%d\n", res.LatencyP95Ms)
	fmt.Printf("audio_rate_hz=%d\n", f.targetRate)
	fmt.Printf("layout=%s\n", targetLayout)
	fmt.Printf("cache_source_verified=%v\n", res.CacheSourceChecked)
	fmt.Printf("color_state=%s\n", res.ColorSummary)
	fmt.Printf("recovered_pkts=%d\n", res.VideoStats.Recovered+res.AudioStats.Recovered)
	fmt.Printf("lost_pkts=%d\n", res.VideoStats.Lost+res.AudioStats.Lost)
	fmt.Printf("output_sha256=%s\n", res.OutputDigest)

	if f.resumeCheck {
		dirB, err := os.MkdirTemp("", "mediacore-resume")
		if err != nil {
			return media.Wrap(media.CodeInternal, "demo", -1, err, "mktemp")
		}
		defer os.RemoveAll(dirB)
		cfgB, err := build(dirB, 15) // crash mid-stream
		if err != nil {
			return err
		}
		crashed, err := exec(cfgB)
		if err != nil {
			return err
		}
		if !crashed.Crashed {
			return media.Fault(media.CodeInternal, "demo", -1, "crash simulation did not trigger")
		}
		cfgC, err := build(dirB, 0) // resume in the same directory
		if err != nil {
			return err
		}
		resumed, err := exec(cfgC)
		if err != nil {
			return err
		}
		store, _ := pipeline.NewStore(filepath.Join(dirB, "ckpt"))
		cp, found, err := store.Load()
		if err != nil || !found {
			return media.Fault(media.CodeCheckpointCorrupt, "demo", -1, "no checkpoint after resume")
		}
		consistent := resumed.OutputDigest == res.OutputDigest && cp.Digest == res.OutputDigest
		fmt.Printf("resume_consistent=%v\n", consistent)
		fmt.Printf("checkpoint_digest=%s\n", cp.Digest)
		if !consistent {
			return media.Fault(media.CodeInternal, "demo", -1,
				"resume digest %s / checkpoint %s != clean %s",
				resumed.OutputDigest, cp.Digest, res.OutputDigest)
		}
	}
	return nil
}
