// Package batch 编排单素材流水线并汇总批次报告：
// 容器解析 -> 时间线合成 -> 有界流变换 -> 验证及帧、清单证据输出。
package batch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"mq/internal/container"
	"mq/internal/evidence"
	"mq/internal/stream"
	"mq/internal/timeline"
)

// Config 为批次运行参数。
type Config struct {
	TargetBps     int64
	QueueCapacity int
	SlowConsumer  time.Duration
	MaxInputBytes int64
	MaxTracks     int
}

// 素材分类。
const (
	ClassOK          = "ok"
	ClassRecoverable = "recoverable"
	ClassFatal       = "fatal"
)

// AssetResult 为单素材处理结果。
type AssetResult struct {
	File  string
	Class string
	Err   string
}

// Report 为批次汇总报告。
type Report struct {
	FilesOK         int
	Recoverable     int
	Fatal           int
	TargetBps       int64
	ActualBps       int64
	BitrateDeltaPct float64
	Frames          int
	MaxAlignMs      float64
	MeanAlignMs     float64
	QualityMean     float64
	QualityMin      float64
	ManifestEntries int
	PeakQueue       int64
	FrameHash       string
	Manifest        string
	Assets          []AssetResult
}

// Run 对一批素材文件执行完整流水线并汇总证据。
// 单个素材的失败（无论可恢复或致命）不污染其余素材的结果。
func Run(ctx context.Context, files []string, cfg Config) *Report {
	rep := &Report{TargetBps: cfg.TargetBps}
	var allFrames []evidence.FrameSummary
	var allEntries []evidence.Entry
	var totalBytes int64
	var totalDur timeline.Rat
	frameIdx := 0

	for _, f := range files {
		frames, entries, bytes, dur, peak, err := processOne(ctx, f, cfg)
		class, msg := classify(err)
		rep.Assets = append(rep.Assets, assetResult(f, class, msg))
		switch class {
		case ClassFatal:
			rep.Fatal++
			continue
		case ClassRecoverable:
			rep.Recoverable++
			continue
		}
		rep.FilesOK++
		for i := range frames {
			frames[i].Index = frameIdx + i
		}
		for i := range entries {
			entries[i].Index = frameIdx + i
		}
		frameIdx += len(frames)
		allFrames = append(allFrames, frames...)
		allEntries = append(allEntries, entries...)
		totalBytes += bytes
		totalDur = totalDur.Add(dur)
		if peak > rep.PeakQueue {
			rep.PeakQueue = peak
		}
	}

	// 汇总证据：对齐、码率、质量、清单与帧哈希。
	align, _ := evidence.CheckAlignment(allFrames, allEntries)
	rep.MaxAlignMs = align.MaxMs
	rep.MeanAlignMs = align.MeanMs
	br := evidence.CheckBitrate(totalBytes, totalDur, cfg.TargetBps)
	rep.ActualBps = br.ActualBps
	rep.BitrateDeltaPct = br.DeltaPct
	q := evidence.CheckQuality(allFrames)
	rep.QualityMean = q.Mean
	rep.QualityMin = q.Min
	rep.Frames = len(allFrames)
	rep.ManifestEntries = len(allEntries)
	rep.Manifest = evidence.MarshalManifest(allEntries)
	rep.FrameHash = fmt.Sprintf("%x", evidence.FramesHash(allFrames))
	return rep
}

// framesOffset 已内联：条目与帧按同一基准重编号。
func assetResult(file, class, msg string) AssetResult {
	if class == ClassOK {
		return AssetResult{File: file, Class: class}
	}
	return AssetResult{File: file, Class: class, Err: msg}
}

// classify 将错误分类为 ok / recoverable / fatal。
func classify(err error) (string, string) {
	if err == nil {
		return ClassOK, ""
	}
	var terr *timeline.Error
	if errors.As(err, &terr) && terr.Fatal {
		return ClassFatal, err.Error()
	}
	return ClassRecoverable, err.Error()
}

// processOne 处理单个素材，返回证据与统计；任何阶段错误都会中止该素材。
func processOne(ctx context.Context, file string, cfg Config) (
	frames []evidence.FrameSummary, entries []evidence.Entry,
	totalBytes int64, dur timeline.Rat, peak int64, err error,
) {
	buf, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, timeline.NewFatal(
			timeline.StageParse, 0, "read %s: %v", file, err)
	}
	asset, err := container.Parse(buf, container.ParseOptions{
		MaxBytes:  cfg.MaxInputBytes,
		MaxTracks: cfg.MaxTracks,
	})
	if err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, err
	}
	tl, err := timeline.Compose(asset, timeline.ComposeOptions{})
	if err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, err
	}
	sres := stream.Run(ctx, stream.SamplesSource(tl, asset), stream.Options{
		QueueCapacity: cfg.QueueCapacity,
		SlowConsumer:  cfg.SlowConsumer,
	})
	if sres.Err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, sres.Err
	}
	frames, err = evidence.ExportFrames(tl, asset)
	if err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, err
	}
	if err := evidence.CheckFrames(frames); err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, err
	}
	if err := evidence.CheckTransform(tl); err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, err
	}
	entries = evidence.BuildManifest(tl)
	if _, err := evidence.CheckAlignment(frames, entries); err != nil {
		return nil, nil, 0, timeline.Rat{}, 0, err
	}
	return frames, entries, tl.TotalSampleBytes(), tl.DurationRat(), sres.PeakQueue, nil
}
