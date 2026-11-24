// Command verify runs the local self-generated media kernel acceptance check.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/media-kernel/codec"
	"example.com/media-kernel/media"
	"example.com/media-kernel/pipeline"
	"example.com/media-kernel/provenance"
	"example.com/media-kernel/sample"
	"example.com/media-kernel/state"
)

type runParameters struct {
	Codec          string `json:"codec"`
	MaxMemoryBytes int64  `json:"max_memory_bytes"`
	QueueCapacity  int    `json:"queue_capacity"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "VERIFY FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("VERIFY PASS: all assertions succeeded")
}

func run() error {
	selfGenerate := flag.Bool("self-generate", false, "generate deterministic local fixtures")
	maxMemory := flag.String("max-memory", "8MiB", "tracked memory cap (e.g. 8MiB)")
	checkpointPath := flag.String("checkpoint", ".tmp/checkpoint.json", "checkpoint JSON path")
	flag.Parse()

	if !*selfGenerate {
		return errors.New("this acceptance entry currently requires --self-generate")
	}
	limit, err := pipeline.ParseSize(*maxMemory)
	if err != nil {
		return err
	}
	params := runParameters{Codec: string(codec.TypeOpus), MaxMemoryBytes: limit, QueueCapacity: 4}
	ctx := context.Background()

	fixture, err := sample.Generate()
	if err != nil {
		return fmt.Errorf("generate fixtures: %w", err)
	}
	result, err := pipeline.AnalyzeFixture(ctx, fixture, pipeline.Options{
		Codec:          codec.TypeOpus,
		MaxMemoryBytes: limit,
		QueueCapacity:  params.QueueCapacity,
	})
	if err != nil {
		return fmt.Errorf("analyze fixture: %w", err)
	}

	// Also exercise the AAC duration parser in the terminal acceptance path.
	aacInfo, err := codec.ParseAAC(codec.BuildADTSFrame(3, 2, 0, 32))
	if err != nil {
		return fmt.Errorf("parse AAC sample: %w", err)
	}

	paramsHash, err := provenance.HashParameters(params)
	if err != nil {
		return err
	}
	store := &state.Store{Path: *checkpointPath}
	recovery, err := pipeline.SimulateInterruptAndResume(store, result, paramsHash, 2)
	if err != nil {
		return fmt.Errorf("checkpoint recovery: %w", err)
	}
	if err := pipeline.VerifyIdempotentRetry(store, result, paramsHash); err != nil {
		return fmt.Errorf("idempotent retry: %w", err)
	}

	manifestPath := filepath.Join(filepath.Dir(*checkpointPath), "provenance.json")
	if err := writeManifest(manifestPath, fixture, params, paramsHash, result); err != nil {
		return err
	}

	printReport(fixture, result, recovery, aacInfo, *checkpointPath, manifestPath, limit)
	return assertReport(fixture, result, recovery, limit)
}

func writeManifest(path string, f *sample.Fixture, params runParameters, paramsHash string, r *pipeline.Result) error {
	var inputSize int64
	for _, b := range f.RawRTP {
		inputSize += int64(len(b))
	}
	inputSize += int64(len(f.NormalMP4) + len(f.CorruptMP4))
	manifest, err := provenance.NewManifest(provenance.Source{
		Name:      f.Name,
		Kind:      "self-generated/rtp-opus-aac+mp4",
		SizeBytes: inputSize,
		SHA256:    f.InputHash,
		Generated: true,
	}, params)
	if err != nil {
		return err
	}
	manifest.ParametersHash = paramsHash
	manifest.Segments = r.Segments
	manifest.Errors = collectErrors(r)
	manifest.Metrics["round_trip_rate"] = r.NormalMP4.RoundTrip.Rate
	manifest.Metrics["keyframe_hit_rate"] = r.NormalMP4.Index.HitRate()
	manifest.Metrics["missing_packets"] = len(r.MissingSequences)
	manifest.Metrics["damage_regions"] = len(r.Damage.Regions)
	manifest.Metrics["peak_tracked_bytes"] = r.PeakTrackedBytes
	b, err := provenance.CanonicalJSON(manifest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func collectErrors(r *pipeline.Result) []*media.Error {
	var out []*media.Error
	for _, a := range r.Audio {
		if a.Error != nil {
			out = append(out, a.Error)
		}
	}
	if r.NormalMP4 != nil && r.NormalMP4.Index != nil {
		out = append(out, r.NormalMP4.Index.Errors...)
	}
	if r.CorruptMP4 != nil && r.CorruptMP4.Index != nil {
		out = append(out, r.CorruptMP4.Index.Errors...)
	}
	return out
}

func printReport(f *sample.Fixture, r *pipeline.Result, rec *pipeline.RecoveryReport, aac *codec.Info, cpPath, manifestPath string, limit int64) {
	rt := r.NormalMP4.RoundTrip
	idx := r.NormalMP4.Index
	fmt.Println("=== 媒体内核验收报告 ===")
	fmt.Printf("输入来源: %s generated=true input_sha256=%s\n", f.Name, f.InputHash)
	fmt.Printf("解析版本: %s\n", provenance.ParseVersion)
	fmt.Printf("RTP: 重复包=%d 缺包数=%d 缺失扩展序号=%v\n", r.DuplicatePackets, len(r.MissingSequences), r.MissingSequences)
	fmt.Printf("AAC 样例: samples=%d rate=%dHz duration=%s\n", aac.Samples, aac.SampleRateHz, aac.Duration())
	fmt.Printf("MP4 box 往返解析一致率: %.1f%% (%d/%d boxes)\n", rt.Rate*100, rt.Consistent, rt.Checked)
	fmt.Printf("关键帧索引命中率: %.1f%% (%d/%d)\n", idx.HitRate()*100, idx.HitCount, idx.IndexedCount)
	fmt.Println("逐片段 SHA-256:")
	for _, seg := range r.Segments {
		fmt.Printf("  %s  time=%d..%d/%d  bytes=[%d,%d)  %s\n",
			seg.ID, seg.StartTicks, seg.EndTicks, seg.Timebase.Denominator,
			seg.ByteRange.Start, seg.ByteRange.End, seg.SHA256)
	}
	fmt.Printf("缺包数: %d\n", len(r.MissingSequences))
	fmt.Println("损坏区间:")
	for _, d := range r.Damage.Regions {
		loc := []string{string(d.Kind), string(d.Code), d.Message}
		if d.FirstSeq != nil {
			loc = append(loc, fmt.Sprintf("seq=%d..%d", *d.FirstSeq, *d.LastSeq))
		}
		if d.TimeRange != nil {
			loc = append(loc, fmt.Sprintf("time=%d..%d(%s)",
				d.TimeRange.Start.Ticks, d.TimeRange.End.Ticks, d.TimeRange.Duration()))
		}
		if d.ByteRange != nil {
			loc = append(loc, fmt.Sprintf("bytes=[%d,%d)", d.ByteRange.Start, d.ByteRange.End))
		}
		fmt.Printf("  %s %s\n", d.ID, strings.Join(loc, " "))
	}
	fmt.Printf("峰值内存: tracked=%d bytes (%s), cap=%d bytes (%s)\n",
		r.PeakTrackedBytes, humanBytes(r.PeakTrackedBytes), limit, humanBytes(limit))
	fmt.Printf("恢复点: interrupted_after=%d resumed_from=%d path=%s\n",
		rec.InterruptedAfter, rec.ResumedFrom, cpPath)
	fmt.Println("恢复前后哈希:")
	for i := 0; i < rec.InterruptedAfter; i++ {
		before := rec.BeforeHashes[i]
		after := rec.AfterHashes[i]
		fmt.Printf("  segment-%03d before=%s after=%s consistent=%v\n", i, before, after, before == after)
	}
	fmt.Printf("幂等重试: checkpoint completed and all %d segment hashes match\n", len(r.Segments))
	fmt.Printf("来源清单: %s\n", manifestPath)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.2fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2fKiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
