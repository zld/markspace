package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediqc/internal/cache"
	"mediqc/internal/clock"
	"mediqc/internal/fault"
	"mediqc/internal/manifest"
	"mediqc/internal/segment"
	"mediqc/internal/subtitle"
)

type demoConfig struct {
	seed          int64
	resumeCheck   bool
	slowSinkMs    int
	maxBufferByte int64
}

const chunkSize = 32 * 1024

func demoCmd(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	cfg := demoConfig{}
	fs.Int64Var(&cfg.seed, "seed", 42, "deterministic asset seed")
	fs.BoolVar(&cfg.resumeCheck, "resume-check", false, "interrupt and resume, compare manifests")
	fs.IntVar(&cfg.slowSinkMs, "slow-sink-ms", 0, "per-chunk sink latency in ms")
	fs.Int64Var(&cfg.maxBufferByte, "max-buffer-bytes", 256*1024, "pipeline buffer limit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if cfg.maxBufferByte < chunkSize {
		fmt.Fprintln(os.Stderr, "max-buffer-bytes must be >= chunk size", chunkSize)
		return 2
	}
	if err := runDemo(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		return 1
	}
	return 0
}

// workItem is one (representation, segment) unit of work.
type workItem struct {
	rep   repFixture
	seg   segment.Segment
	src   segment.KeyframeSource
	index int
}

func runDemo(cfg demoConfig) error {
	work, err := os.MkdirTemp("", "mediqc-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	fx := makeFixture(cfg.seed)

	m := map[string]string{}
	var failed []string
	check := func(name string, ok bool, value string) {
		m[name] = value
		if !ok {
			failed = append(failed, name)
		}
	}

	// 1. Subtitle parse round-trip.
	parseOK, parseTotal := 0, 0
	for _, format := range []string{"srt", "vtt"} {
		in := fx.SRT
		if format == "vtt" {
			in = fx.VTT
		}
		tl, err := subtitle.Parse(format, strings.NewReader(in))
		if err != nil {
			return fmt.Errorf("parse %s: %w", format, err)
		}
		tl2, err := subtitle.Parse(format, strings.NewReader(subtitle.Serialize(tl)))
		if err != nil {
			return fmt.Errorf("reparse %s: %w", format, err)
		}
		for i := range tl.Cues {
			parseTotal++
			if i < len(tl2.Cues) && tl.Cues[i].Start == tl2.Cues[i].Start &&
				tl.Cues[i].End == tl2.Cues[i].End && tl.Cues[i].Text == tl2.Cues[i].Text {
				parseOK++
			}
		}
	}
	parsePct := 100 * float64(parseOK) / float64(parseTotal)
	check("parse_roundtrip", parsePct == 100.0, fmt.Sprintf("%.2f%%", parsePct))

	// 2. Timecode round-trip across all supported rates (incl. 29.97 DF).
	tcOK, tcTotal := 0, 0
	for _, r := range clock.Supported() {
		for _, drop := range []bool{false, r.DropCapable()} {
			for f := int64(0); f < 50000; f += 499 {
				tcTotal++
				tc, err := clock.FramesToTimecode(f, r, drop)
				if err != nil {
					continue
				}
				if back, err := clock.TimecodeToFrames(tc); err == nil && back == f {
					tcOK++
				}
			}
		}
	}
	tcPct := 100 * float64(tcOK) / float64(tcTotal)
	check("timecode_roundtrip", tcPct == 100.0, fmt.Sprintf("%.2f%%", tcPct))

	// 3. Keyframe extraction: stss index, sample scan, synthetic.
	kfSources := map[string]segment.KeyframeSource{}
	repKFs := map[string][]int64{}
	for i, rep := range fx.Reps {
		var kfs []int64
		var src segment.KeyframeSource
		switch i {
		case 0: // stss index
			p := filepath.Join(work, rep.Name+".mp4")
			if err := writeMP4(p, rep.Keyframes, rep.TotalFrame, mp4Index); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			kfs, src, err = segment.ScanKeyframes(f)
			f.Close()
			if err != nil {
				return err
			}
		case 1: // index missing -> sample scan
			p := filepath.Join(work, rep.Name+".mp4")
			if err := writeMP4(p, rep.Keyframes, rep.TotalFrame, mp4Scan); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			kfs, src, err = segment.ScanKeyframes(f)
			f.Close()
			if err != nil {
				return err
			}
		default: // synthetic in-memory index
			kfs, src = rep.Keyframes, "synthetic"
		}
		kfSources[rep.Name] = src
		repKFs[rep.Name] = kfs
		if len(kfs) != len(rep.Keyframes) {
			return fmt.Errorf("rep %s: extracted %d keyframes, want %d", rep.Name, len(kfs), len(rep.Keyframes))
		}
	}
	m["keyframe_source_v24"] = string(kfSources["v24"])
	m["keyframe_source_v25"] = string(kfSources["v25"])

	// 4. Segment plan + cross-representation alignment.
	var items []workItem
	var reps []segment.Representation
	for _, rep := range fx.Reps {
		reps = append(reps, segment.Representation{
			Name: rep.Name, Rate: rep.Rate, Keyframes: repKFs[rep.Name]})
	}
	cuts, err := segment.Align(reps, fixtureDurationTicks, fixtureSegTargetTick)
	if err != nil {
		return err
	}
	m["aligned_cuts"] = fmt.Sprintf("%d", len(cuts))
	hitNum, hitDen := 0, 0
	for i, rep := range fx.Reps {
		segs, err := segment.Plan(repKFs[rep.Name], rep.TotalFrame,
			clock.TicksToFrames(fixtureSegTargetTick, rep.Rate))
		if err != nil {
			return err
		}
		if i == 0 {
			hitNum = int(segment.KeyframeHitRate(segs) * float64(len(segs)))
			hitDen = len(segs)
		}
		for _, s := range segs {
			items = append(items, workItem{rep: rep, seg: s, src: kfSources[rep.Name]})
		}
	}
	hitPct := 100 * float64(hitNum) / float64(hitDen)
	check("keyframe_hit", hitPct >= 99.0, fmt.Sprintf("%.2f%%", hitPct))

	// 5. Stream segments with checkpointing; clean run first.
	cacheStore, err := cache.Open(filepath.Join(work, "cache"))
	if err != nil {
		return err
	}
	params := fmt.Sprintf("seed=%d;gop=2s;rates=24,25,50", cfg.seed)
	origin := fmt.Sprintf("seed-%d", cfg.seed)
	manifestA, peakA, err := runSegments(context.Background(), cfg, items,
		filepath.Join(work, "cp-clean"), cacheStore, params, origin, -1)
	if err != nil {
		return err
	}
	peak := peakA

	// 6. Resume check: interrupt, then resume from checkpoint.
	resumeEqual := true
	if cfg.resumeCheck {
		cpDir := filepath.Join(work, "cp-resume")
		cancelAt := len(items) / 2
		ctx, cancel := context.WithCancel(context.Background())
		_, _, err := runSegments(ctx, cfg, items, cpDir, cacheStore, params, origin, cancelAt)
		cancel()
		if !fault.IsCode(err, fault.CodeCancelled) {
			return fmt.Errorf("interrupted run: want E_CANCELLED, got %v", err)
		}
		manifestB, peakB, err := runSegments(context.Background(), cfg, items,
			cpDir, cacheStore, params, origin, -1)
		if err != nil {
			return err
		}
		if peakB > peak {
			peak = peakB
		}
		eq, err := manifest.Equal(manifestA, manifestB)
		if err != nil {
			return err
		}
		resumeEqual = eq
	}
	check("resume_manifest_equal", !cfg.resumeCheck || resumeEqual,
		fmt.Sprintf("%v", resumeEqual))
	check("peak_buffer_bytes", peak <= cfg.maxBufferByte, fmt.Sprintf("%d", peak))
	check("segment_hashes", len(manifestA.Segments) >= 12,
		fmt.Sprintf("%d", len(manifestA.Segments)))

	// 7. Corrupted fixtures: truncated subtitle, illegal DF marker,
	//    missing keyframe index, cache digest tamper.
	rejected := 0
	if _, err := subtitle.ParseSRT(strings.NewReader(
		"1\n00:00:01,000 --> 00:00:02,000\n")); fault.IsCode(err, fault.CodeTruncatedInput) {
		rejected++
	}
	if _, err := clock.ParseTimecode("00:01:00;01", clock.FPS2997); fault.IsCode(err, fault.CodeBadDropFrame) {
		rejected++
	}
	corruptMP4 := filepath.Join(work, "corrupt.mp4")
	if err := writeMP4(corruptMP4, nil, 0, mp4None); err != nil {
		return err
	}
	if f, err := os.Open(corruptMP4); err == nil {
		_, _, err = segment.ScanKeyframes(f)
		f.Close()
		if fault.IsCode(err, fault.CodeMissingKeyframe) {
			rejected++
		}
	}
	recomputed := 0
	tamperKey := cache.Key([]byte("tamper-fixture"), "24/1", params)
	if err := cacheStore.Put(tamperKey, origin, []byte("digest-a")); err != nil {
		return err
	}
	bin := filepath.Join(work, "cache", tamperKey+".bin")
	if err := os.WriteFile(bin, []byte("forged-bytes"), 0o644); err != nil {
		return err
	}
	if _, hit, tampered, err := cacheStore.Get(tamperKey, origin); err == nil && !hit && tampered {
		rejected++
		// Quarantined: recompute from source and store again.
		if err := cacheStore.Put(tamperKey, origin, []byte("digest-a")); err != nil {
			return err
		}
		if got, hit, tampered, _ := cacheStore.Get(tamperKey, origin); hit && !tampered && string(got) == "digest-a" {
			recomputed++
		}
	}
	check("corrupted_rejected", rejected == 4, fmt.Sprintf("%d", rejected))
	check("cache_tamper_recomputed", recomputed == 1, fmt.Sprintf("%d", recomputed))

	// Report.
	order := []string{"parse_roundtrip", "timecode_roundtrip", "keyframe_hit",
		"keyframe_source_v24", "keyframe_source_v25", "aligned_cuts",
		"segment_hashes", "resume_manifest_equal", "peak_buffer_bytes",
		"corrupted_rejected", "cache_tamper_recomputed"}
	for _, k := range order {
		fmt.Printf("%s=%s\n", k, m[k])
	}
	if len(failed) > 0 {
		return fmt.Errorf("checks failed: %s", strings.Join(failed, ","))
	}
	fmt.Println("result=PASS")
	return nil
}
