// mediarun 为媒资恢复处理内核的终端入口。
// 子命令：demo（生成样例并执行）、resume（断点续跑）。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediarun/internal/checkpoint"
	"mediarun/internal/ebml"
	"mediarun/internal/pipeline"
	"mediarun/internal/testkit"
)

// 退出码：0 成功；1 测试/指标不达标；2 输入或参数摘要不匹配；3 解析失败；10 中断。
const (
	exitOK        = 0
	exitMetric    = 1
	exitMismatch  = 2
	exitParse     = 3
	exitInterrupt = 10
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mediarun <demo|resume> [flags]")
		return exitMetric
	}
	switch args[0] {
	case "demo":
		return cmdRun(args[1:], true)
	case "resume":
		return cmdRun(args[1:], false)
	}
	fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
	return exitMetric
}

func cmdRun(args []string, isDemo bool) int {
	fs := flag.NewFlagSet("mediarun", flag.ContinueOnError)
	seed := fs.Int64("seed", 7, "样例种子")
	rate := fs.Int("rate", 48000, "目标采样率")
	layout := fs.String("layout", "stereo", "声道布局 mono|stereo")
	lufs := fs.Float64("lufs", -16, "目标响度 LUFS")
	loss := fs.Float64("loss", 0.02, "RTP 丢包率")
	interruptAfter := fs.String("interrupt-after", "", "在指定阶段后中断(parse/audio/rtp)")
	workDir := fs.String("work-dir", ".mediarun", "工作目录")
	rtpWindow := fs.Int("rtp-window", 8, "RTP 恢复窗口")
	queueCap := fs.Int("queue-cap", 4, "阶段队列容量上限")
	if err := fs.Parse(args); err != nil {
		return exitMetric
	}
	dir := filepath.Join(*workDir, fmt.Sprintf("seed-%d", *seed))
	params := pipeline.Params{
		Seed: *seed, Rate: *rate, Layout: *layout, LUFS: *lufs,
		Loss: *loss, RTPWindow: *rtpWindow, QueueCap: *queueCap,
	}
	if isDemo {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fail(err)
		}
		b, _ := json.MarshalIndent(params, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "params.json"), b, 0o644); err != nil {
			return fail(err)
		}
	} else {
		b, err := os.ReadFile(filepath.Join(dir, "params.json"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "no params to resume; run demo first")
			return exitMismatch
		}
		if err := json.Unmarshal(b, &params); err != nil {
			return fail(err)
		}
	}
	inputs, err := testkit.Ensure(dir, params.Seed, params.Loss)
	if err != nil {
		return fail(err)
	}
	if isDemo {
		fmt.Fprintf(os.Stderr, "样例: 正常/截断簇/超窗丢包/背压 media=%s rtp=%s\n",
			inputs.MediaPath, inputs.RTPPath)
	}
	r := &pipeline.Runner{
		Params: params, WorkDir: dir,
		MediaPath: inputs.MediaPath, RTPPath: inputs.RTPPath,
		MediaSHA: inputs.MediaSHA256, RTPSHA: inputs.RTPSHA256,
	}
	if isDemo {
		r.InterruptAfter = *interruptAfter
	}
	res, err := r.Run()
	if err != nil {
		return mapError(err)
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if v := validate(res); len(v) > 0 {
		for _, s := range v {
			fmt.Fprintln(os.Stderr, "指标不达标:", s)
		}
		return exitMetric
	}
	return exitOK
}

func mapError(err error) int {
	switch {
	case errors.Is(err, pipeline.ErrInterrupted):
		fmt.Fprintln(os.Stderr, "interrupted: checkpoint saved")
		return exitInterrupt
	case errors.Is(err, checkpoint.ErrMismatch):
		fmt.Fprintln(os.Stderr, "refuse resume:", err)
		return exitMismatch
	}
	var ee *ebml.Error
	if errors.As(err, &ee) {
		fmt.Fprintln(os.Stderr, "parse failure:", ee)
		return exitParse
	}
	return fail(err)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return exitMetric
}

// validate 校验验收指标阈值。
func validate(r *pipeline.Result) []string {
	var bad []string
	if r.AVSyncMS > 20 {
		bad = append(bad, fmt.Sprintf("av_sync_ms=%.3f > 20", r.AVSyncMS))
	}
	if r.PacketRecoveryRate < 0.95 {
		bad = append(bad, fmt.Sprintf("packet_recovery_rate=%.4f < 0.95", r.PacketRecoveryRate))
	}
	if r.LatencyP95MS > 100 {
		bad = append(bad, fmt.Sprintf("latency_p95_ms=%.3f > 100", r.LatencyP95MS))
	}
	if r.MemoryPeakBytes > 67108864 {
		bad = append(bad, fmt.Sprintf("memory_peak_bytes=%d > 67108864", r.MemoryPeakBytes))
	}
	return bad
}
