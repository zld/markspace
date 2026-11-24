// Command verify is the terminal entry point for batch checks, segment
// manifests, alignment and resume demonstrations.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zld/mediapipe/fixtures"
	"github.com/zld/mediapipe/media"
	"github.com/zld/mediapipe/runner"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		generate       = fs.String("generate", "", "write sample inputs to this directory")
		input          = fs.String("input", "", "input file or sample directory")
		segment        = fs.String("segment", "2s", "target segment duration")
		bitrates       = fs.String("bitrates", "", "comma-separated representation bitrates")
		report         = fs.String("report", "text", "report format: text or json")
		work           = fs.String("work", "", "work directory for indexes, segments and checkpoints")
		resume         = fs.Bool("resume", false, "resume a previously interrupted run")
		interruptAfter = fs.Int("interrupt-after", 0, "exit with code 10 after N committed segments")
		rtpWait        = fs.Int("rtp-wait", 0, "RTP arrivals to wait before declaring a gap")
		rtpWindow      = fs.Int("rtp-window", 0, "RTP reorder window size")
		maxDeviation   = fs.String("max-deviation", "", "maximum layer boundary deviation")
	)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return runner.ExitArgument
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "argument error: unexpected arguments %v\n", fs.Args())
		return runner.ExitArgument
	}

	if *generate != "" {
		return generateSamples(*generate)
	}
	if *input == "" {
		fmt.Fprintf(os.Stderr, "argument error: --input or --generate is required\n")
		return runner.ExitArgument
	}
	if *report != "json" && *report != "text" {
		fmt.Fprintf(os.Stderr, "argument error: --report must be text or json\n")
		return runner.ExitArgument
	}

	seg, err := media.ParseTime(*segment)
	if err != nil || seg <= 0 {
		fmt.Fprintf(os.Stderr, "argument error: invalid segment duration %q\n", *segment)
		return runner.ExitArgument
	}

	var maxDev media.Time
	if *maxDeviation != "" {
		v, err := media.ParseTime(*maxDeviation)
		if err != nil || v < 0 {
			fmt.Fprintf(os.Stderr, "argument error: invalid max deviation %q\n", *maxDeviation)
			return runner.ExitArgument
		}
		maxDev = v
	} else {
		maxDev = 0 // runner derives one and a half reference frames
	}

	inputs, err := collectInputs(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return runner.ExitArgument
	}
	if len(inputs) == 0 {
		fmt.Fprintf(os.Stderr, "no MP4/MOV inputs found under %s\n", *input)
		return runner.ExitFailures
	}

	workDir := *work
	if workDir == "" {
		if abs, err := filepath.Abs(*input); err == nil {
			workDir = filepath.Join(filepath.Dir(abs), "work")
		}
	}
	if workDir == "" {
		fmt.Fprintf(os.Stderr, "argument error: cannot derive work directory; pass --work\n")
		return runner.ExitArgument
	}

	var brs []string
	for _, b := range strings.Split(*bitrates, ",") {
		b = strings.TrimSpace(b)
		if b != "" {
			brs = append(brs, b)
		}
	}

	opts := runner.Options{
		Inputs: inputs, WorkDir: workDir, Segment: seg, Bitrates: brs,
		MaxDeviation: maxDev, Resume: *resume,
		InterruptAfter: *interruptAfter, RTPWait: *rtpWait, RTPWindow: *rtpWindow,
	}
	rep, code := runner.Run(opts)
	if *report == "json" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "internal error: %v\n", err)
			return runner.ExitInternal
		}
		os.Stdout.Write(b)
		os.Stdout.Write([]byte("\n"))
	} else {
		printText(rep)
	}
	return code
}

func collectInputs(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("cannot read input %s: %w", root, err)
	}
	if !info.IsDir() {
		if isMediaPath(root) {
			return []string{root}, nil
		}
		return nil, fmt.Errorf("%s is not an MP4/MOV file", root)
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Never descend into the work directory or temp artifacts.
			if d.Name() == "work" || d.Name() == "segments" || d.Name() == "index" {
				return filepath.SkipDir
			}
			return nil
		}
		if isMediaPath(path) && !isRepresentationPath(path) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func isMediaPath(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".mp4" || ext == ".mov"
}

// isRepresentationPath reports whether a file is a per-bitrate representation
// of a sibling input (e.g. "baseline.500k.mp4"). Those are consumed as layers
// of their base input, not as independent batch entries.
func isRepresentationPath(p string) bool {
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	parts := strings.Split(base, ".")
	if len(parts) < 2 {
		return false
	}
	last := strings.ToLower(parts[len(parts)-1])
	last = strings.TrimPrefix(last, "video-")
	if !strings.HasSuffix(last, "k") && !strings.HasSuffix(last, "m") {
		return false
	}
	_, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(last, "k"), "m"))
	return err == nil
}

func generateSamples(dir string) int {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "internal error: %v\n", err)
		return runner.ExitInternal
	}
	good := filepath.Join(dir, "good")
	corrupt := filepath.Join(dir, "corrupt")

	// Representation variants share the reference video timing but shift
	// their keyframe grid by one sample, so cross-bitrate alignment has a
	// real (sub-frame) deviation to measure.
	variant := func(name string, phase int) fixtures.Spec {
		s := fixtures.DefaultSpec(name)
		s.Tracks = s.Tracks[:1]
		s.Tracks[0].SyncOffsetPhase = phase
		return s
	}
	specsGood := []fixtures.Spec{
		fixtures.DefaultSpec("baseline.mp4"),
		func() fixtures.Spec { s := fixtures.DefaultSpec("baseline.mov"); s.Corrupt = "mov"; return s }(),
		variant("baseline.500k.mp4", 0),
		variant("baseline.1000k.mp4", 1),
	}
	specsCorrupt := []fixtures.Spec{
		func() fixtures.Spec {
			s := fixtures.DefaultSpec("truncated.mp4")
			s.Corrupt = "truncate_moov"
			return s
		}(),
		func() fixtures.Spec {
			s := fixtures.DefaultSpec("corrupt_box.mp4")
			s.Corrupt = "corrupt_box"
			s.CorruptBox = "stco"
			return s
		}(),
		func() fixtures.Spec { s := fixtures.DefaultSpec("zero_duration.mp4"); s.ZeroDuration = true; return s }(),
		func() fixtures.Spec {
			s := fixtures.DefaultSpec("wraparound.mp4")
			s.Tracks = s.Tracks[:1]
			s.WrapAt = 20
			return s
		}(),
		func() fixtures.Spec {
			s := fixtures.DefaultSpec("gap.mp4")
			s.FrameDropEvery = 20
			return s
		}(),
	}

	var failures int
	for _, spec := range specsGood {
		if _, err := fixtures.Generate(spec, good); err != nil {
			fmt.Fprintf(os.Stderr, "generate %s: %v\n", spec.Name, err)
			failures++
		}
	}
	for _, spec := range specsCorrupt {
		if _, err := fixtures.Generate(spec, corrupt); err != nil {
			fmt.Fprintf(os.Stderr, "generate %s: %v\n", spec.Name, err)
			failures++
		}
	}
	// A dedicated RTP-loss fixture: a media file paired with an arrival
	// sequence that drops a six-packet burst, so the recovery rate is
	// demonstrably below 1.0 and the gap is attributed to a segment.
	lossDir := filepath.Join(dir, "rtp_loss")
	if err := os.MkdirAll(lossDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "internal error: %v\n", err)
		return runner.ExitInternal
	}
	lossSpec := fixtures.DefaultSpec("loss.mp4")
	lossRes, err := fixtures.Generate(lossSpec, lossDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate loss media: %v\n", err)
		failures++
	} else {
		seq := fixtures.BuildRTPBurst(lossSpec.Tracks[0], 20, 6)
		b, err := json.Marshal(seq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate rtp: %v\n", err)
			return runner.ExitInternal
		}
		rtpPath := filepath.Join(lossDir, "loss.rtp.json")
		if err := os.WriteFile(rtpPath, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "generate rtp: %v\n", err)
			failures++
		}
		_ = lossRes
	}

	fmt.Printf("generated %d good, %d corrupt samples under %s\n",
		len(specsGood), len(specsCorrupt), dir)
	if failures > 0 {
		return runner.ExitInternal
	}
	return runner.ExitOK
}

func printText(rep *runner.Report) {
	for _, f := range rep.Files {
		name := filepath.Base(f.Path)
		if f.SHA256 != "" && len(f.SHA256) >= 16 {
			name += " (" + f.SHA256[:16] + ")"
		}
		if f.Error != nil {
			fmt.Printf("FAIL %s [%s] %s\n", name, f.Error.Kind, f.Error.Detail)
			continue
		}
		fmt.Printf("OK   %s segments=%d manifest=%s av_sync=%dms p95=%.3fms\n",
			name, f.SegmentCount, short(f.ManifestSHA256), f.AVSyncErrorMS, f.P95LatencyMS)
	}
	fmt.Printf("summary: ok=%v files=%d failures=%d segments=%d manifest=%s av_sync=%dms p95=%.3fms rtp_rate=%.4f\n",
		rep.OK, len(rep.Files), rep.FailureCount, rep.SegmentCount,
		short(rep.ManifestSHA256), rep.AVSyncErrorMS, rep.P95LatencyMS, rep.PacketRecoveryRate)
	if rep.Interrupted {
		fmt.Printf("interrupted: resume with --resume\n")
	}
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}
