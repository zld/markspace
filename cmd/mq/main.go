// mq 为纯终端媒体质检工具：本入口只负责参数适配，
// 全部处理逻辑在 internal 各子系统中。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"mq/internal/batch"
	"mq/internal/fixtures"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mq demo --out DIR [--target-bps N] [--queue N] [--slow-ms N]")
		return 1
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "输出目录（样例与证据写入此处）")
	targetBps := fs.Int64("target-bps", 8_000_000, "目标码率 (bps)")
	queue := fs.Int("queue", 4, "有界队列容量")
	slowMs := fs.Int("slow-ms", 0, "模拟慢消费者的每包毫秒数")
	maxInputBytes := fs.Int64("max-input-bytes", 64<<20, "单素材输入大小上限 (字节)")
	maxTracks := fs.Int("max-tracks", 8, "单素材轨道数上限")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if *out == "" || *targetBps <= 0 || *queue < 1 || *slowMs < 0 {
		fmt.Fprintln(os.Stderr, "mq: invalid parameters")
		return 1
	}

	// 自生成样例批次（不引用外部文件）。
	files, err := fixtures.WriteAll(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mq: generate fixtures:", err)
		return 1
	}

	rep := batch.Run(context.Background(), files, batch.Config{
		TargetBps:     *targetBps,
		QueueCapacity: *queue,
		SlowConsumer:  time.Duration(*slowMs) * time.Millisecond,
		MaxInputBytes: *maxInputBytes,
		MaxTracks:     *maxTracks,
	})

	for _, a := range rep.Assets {
		if a.Err != "" {
			fmt.Printf("asset=%s class=%s err=%q\n", a.File, a.Class, a.Err)
		} else {
			fmt.Printf("asset=%s class=%s\n", a.File, a.Class)
		}
	}
	fmt.Printf("files_ok=%d\n", rep.FilesOK)
	fmt.Printf("recoverable=%d\n", rep.Recoverable)
	fmt.Printf("fatal=%d\n", rep.Fatal)
	fmt.Printf("target_bps=%d\n", rep.TargetBps)
	fmt.Printf("actual_bps=%d\n", rep.ActualBps)
	fmt.Printf("bitrate_delta_pct=%.2f\n", rep.BitrateDeltaPct)
	fmt.Printf("frames=%d\n", rep.Frames)
	fmt.Printf("max_align_ms=%.3f\n", rep.MaxAlignMs)
	fmt.Printf("mean_align_ms=%.3f\n", rep.MeanAlignMs)
	fmt.Printf("quality_mean=%.3f\n", rep.QualityMean)
	fmt.Printf("quality_min=%.3f\n", rep.QualityMin)
	fmt.Printf("manifest_entries=%d\n", rep.ManifestEntries)
	fmt.Printf("peak_queue=%d\n", rep.PeakQueue)
	fmt.Printf("frame_hash=%s\n", rep.FrameHash)
	return 0
}
