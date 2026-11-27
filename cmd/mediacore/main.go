// Command mediacore is the terminal adapter for the processing kernel. It
// exposes one demo subcommand whose exit status follows the documented
// contract: 0 success, 2 rejected input or quality, 3 resumable interruption.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediacore/internal/bitstream"
	"mediacore/internal/fault"
	"mediacore/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: mediacore demo [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  Generates local synthetic media, then scans, recovers, synchronises,")
	fmt.Fprintln(w, "  adapts and archives it. Every parameter affects the stable digest.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "flags:")
	fmt.Fprintln(w, "  --codec av1|h264|h265    elementary stream codec (default av1)")
	fmt.Fprintln(w, "  --seed N                 deterministic seed (default 17)")
	fmt.Fprintln(w, "  --loss N                 packets to drop (default 3)")
	fmt.Fprintln(w, "  --reorder N              adjacent swaps to apply (default 2)")
	fmt.Fprintln(w, "  --budget-ms N            processing budget (default 5000)")
	fmt.Fprintln(w, "  --quality-min DB         objective quality floor (default 30)")
	fmt.Fprintln(w, "  --target-kbps N          target bitrate (default 900)")
	fmt.Fprintln(w, "  --corrupt-index          plant index faults and expect rejection")
	fmt.Fprintln(w, "  --interrupt-migration    suspend after the verify stage")
	fmt.Fprintln(w, "  --resume                 resume from the rollback point")
	fmt.Fprintln(w, "  --dir PATH               working directory (default .mediacore-demo)")
	fmt.Fprintln(w, "  --json                   print only the JSON report")
	fmt.Fprintln(w, "  --clean                  remove the working directory first")
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mediacore: unknown command %q\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func runDemo(args []string) int {
	opts := pipeline.Defaults()
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	codecFlag := fs.String("codec", string(opts.Codec), "av1, h264 or h265")
	seed := fs.Int64("seed", opts.Seed, "deterministic seed")
	loss := fs.Int("loss", 3, "packets to delay")
	reorder := fs.Int("reorder", 2, "adjacent swaps")
	budget := fs.Int64("budget-ms", opts.BudgetMS, "processing budget in ms")
	qmin := fs.Float64("quality-min", opts.QualityMin, "quality floor in dB")
	target := fs.Float64("target-kbps", opts.TargetKbps, "target bitrate")
	corrupt := fs.Bool("corrupt-index", false, "plant index faults")
	interrupt := fs.Bool("interrupt-migration", false, "suspend after verify")
	resume := fs.Bool("resume", false, "resume from the rollback point")
	dir := fs.String("dir", filepath.Join(".", ".mediacore-demo"), "working directory")
	jsonOnly := fs.Bool("json", false, "print only JSON")
	clean := fs.Bool("clean", false, "remove the working directory first")
	preset := fs.String("preset", "", "encoder preset override")
	width := fs.Int("width", opts.Width, "frame width")
	height := fs.Int("height", opts.Height, "frame height")
	fps := fs.Int("fps", opts.FPS, "frame rate")
	seconds := fs.Int("seconds", opts.Seconds, "clip length")
	waitWindow := fs.Int("wait-window", opts.WaitWindow, "RTP wait window in packets")
	bufferLimit := fs.Int("buffer-limit", opts.BufferLimit, "RTP reorder buffer bytes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout)
			return 0
		}
		return 2
	}
	codec, err := bitstream.ParseCodec(*codecFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	opts.Codec = codec
	opts.Seed = *seed
	opts.Loss = *loss
	opts.Reorder = *reorder
	opts.BudgetMS = *budget
	opts.QualityMin = *qmin
	opts.TargetKbps = *target
	opts.CorruptIndex = *corrupt
	opts.Interrupt = *interrupt
	opts.Resume = *resume
	opts.Preset = *preset
	opts.Width = *width
	opts.Height = *height
	opts.FPS = *fps
	opts.Seconds = *seconds
	opts.WaitWindow = *waitWindow
	opts.BufferLimit = *bufferLimit
	opts.Dir = *dir
	if *clean {
		if err := os.RemoveAll(opts.Dir); err != nil {
			fmt.Fprintf(os.Stderr, "mediacore: clean: %v\n", err)
			return 1
		}
	}
	report, runErr := pipeline.Run(opts)
	if report != nil {
		if *jsonOnly {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(report)
		} else {
			printReport(report)
		}
	}
	if runErr != nil {
		printFault(runErr, *jsonOnly)
		return fault.ExitCode(runErr)
	}
	return 0
}

func printReport(r *pipeline.Report) {
	fmt.Printf("codec=%s seed=%d\n", r.Codec, r.Seed)
	fmt.Printf("target_kbps=%.0f actual_kbps=%.0f deviation_pct=%.2f\n", r.TargetKbps, r.ActualKbps, r.DeviationPct)
	fmt.Printf("psnr_db=%.2f quality_min_db=%.2f\n", r.PSNRdB, r.QualityMin)
	fmt.Printf("scan_frames=%d keyframes=%d index_quarantined=%d\n", r.ScanFrames, r.Keyframes, r.IndexQuarantined)
	fmt.Printf("packets=%d lost=%d recovered_packets=%d missing_packets=%d\n",
		r.Packets, r.LostPackets, r.RecoveredPackets, r.MissingPackets)
	fmt.Printf("sync_offset_ms=%.3f sync_residual_ms=%.3f\n", r.SyncOffsetMS, r.SyncResidualMS)
	fmt.Printf("manifest_entries=%d manifest_time_error_ms=%.3f migration_stage=%s resumed=%v noop=%v\n",
		r.ManifestEntries, r.ManifestTimeErrMS, r.MigrationStage, r.MigrationResumed, r.MigrationNoop)
	fmt.Printf("container video_sec=%.3f audio_sec=%.3f codec=%s drift_ms=%.3f\n",
		r.Container.VideoSec, r.Container.AudioSec, r.Container.CodecName, r.ContainerDriftMS)
	fmt.Printf("archive_digest=%s duplicate=%v\n", r.ArchiveDigest, r.ArchiveDuplicate)
	fmt.Printf("decision=%s reason=%q\n", r.Decision, r.DecisionReason)
	fmt.Printf("digest=%s\n", r.Digest)
}

func printFault(err error, jsonOnly bool) {
	f, ok := fault.As(err)
	if !ok {
		if jsonOnly {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": err.Error(), "exit_status": 1})
			return
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return
	}
	if jsonOnly {
		_ = json.NewEncoder(os.Stderr).Encode(f)
		return
	}
	parts := []string{"code=" + string(f.Code)}
	if f.Codec != "" {
		parts = append(parts, "codec="+f.Codec)
	}
	if f.HasOffset {
		parts = append(parts, fmt.Sprintf("offset=%d", f.Offset))
	}
	parts = append(parts, fmt.Sprintf("exit_status=%d", f.ExitStatus))
	fmt.Fprintf(os.Stderr, "error: %s: %s\n", strings.Join(parts, " "), f.Message)
}
