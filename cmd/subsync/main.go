// Command subsync is a terminal-only offline normalizer for SRT, WebVTT and
// ASS subtitles: it decodes mixed encodings, unifies the cue model, resolves
// overlaps, converts frame rates with integer rational math and verifies the
// result by round-tripping every cue.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"subsync/internal/atomicio"
	"subsync/internal/cli"
	"subsync/internal/decode"
	"subsync/internal/demo"
	"subsync/internal/model"
	"subsync/internal/pipeline"
	"subsync/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := cli.Parse(args, os.Stderr)
	if err != nil {
		if cli.IsUsage(err) {
			fmt.Fprintln(os.Stderr, "error:", err)
			return cli.ExitUsage
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return cli.ExitGeneral
	}

	var raw []byte
	var declaredEncoding string
	if opts.Demo {
		raw, declaredEncoding, err = buildDemoInput(opts)
	} else {
		raw, err = atomicio.ReadLimited(opts.Input)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "io error:", err)
		return cli.ExitIO
	}
	if !opts.Demo && len(raw) > atomicio.MaxInputBytes {
		fmt.Fprintf(os.Stderr, "io error: input exceeds %d MiB limit\n", atomicio.MaxInputBytes/(1024*1024))
		return cli.ExitIO
	}

	rendered, rep, summary, _, err := pipeline.Run(opts, raw, declaredEncoding)
	if err != nil {
		var se *pipeline.StageError
		if errors.As(err, &se) {
			switch se.Stage {
			case "decode", "parse":
				fmt.Fprintln(os.Stderr, se.Stage, "error:", se.Err)
				return cli.ExitDecode
			case "overlap":
				// A policy-triggered failure is a verification-class failure.
				fmt.Fprintln(os.Stderr, "overlap error:", se.Err)
				return cli.ExitThreshold
			case "io":
				fmt.Fprintln(os.Stderr, "io error:", se.Err)
				return cli.ExitIO
			}
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return cli.ExitGeneral
	}

	if opts.Demo {
		if code := writeDemoArtifacts(opts, rendered); code != cli.ExitOK {
			return code
		}
	} else if err := pipeline.WriteOutput(opts.Output, []byte(rendered)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.ExitIO
	}

	dropped := map[string]bool{}
	if opts.JSONL {
		if err := pipeline.EmitReport(os.Stdout, opts, rep, dropped, summary); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return cli.ExitIO
		}
	} else {
		printHuman(opts, summary)
	}

	if pipeline.ThresholdExceeded(rep, summary, opts) {
		fmt.Fprintf(os.Stderr,
			"threshold exceeded: max_delta=%dms (limit %dms), lost=%d (limit %d)\n",
			summary.MaxDeltaMS, opts.MaxDeltaMS, summary.LostCount, opts.MaxDropped)
		return cli.ExitThreshold
	}
	return cli.ExitOK
}

// buildDemoInput materializes the built-in fixture in the requested source
// format and returns its encoded bytes. The acceptance path uses SRT in
// UTF-16LE with BOM.
func buildDemoInput(opts *cli.Options) ([]byte, string, error) {
	switch opts.InputFormat {
	case model.FormatSRT:
		raw, err := demo.EncodeUTF16LE(demo.SRTFixture())
		if err != nil {
			return nil, "", err
		}
		return raw, decode.UTF16LE, nil
	default:
		return nil, "", fmt.Errorf("demo currently supports --input-format srt")
	}
}

func writeDemoArtifacts(opts *cli.Options, rendered string) int {
	dir := opts.WorkDir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "subsync-demo-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "io error:", err)
			return cli.ExitIO
		}
	}
	inPath := filepath.Join(dir, "demo-input."+string(opts.InputFormat))
	outPath := filepath.Join(dir, "demo-output."+string(opts.OutputFormat))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "io error:", err)
		return cli.ExitIO
	}
	inputBytes, _, err := buildDemoInput(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return cli.ExitGeneral
	}
	if err := os.WriteFile(inPath, inputBytes, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "io error:", err)
		return cli.ExitIO
	}
	if err := pipeline.WriteOutput(outPath, []byte(rendered)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.ExitIO
	}
	if !opts.JSONL {
		fmt.Printf("demo artifacts:\n  input:  %s\n  output: %s\n", inPath, outPath)
	}
	return cli.ExitOK
}

func printHuman(opts *cli.Options, s report.SummaryRecord) {
	_ = opts
	fmt.Printf("conversion complete: input=%d output=%d lost=%d overlaps=%d style_loss=%d encoding=%s max_delta=%dms\n",
		s.InputCount, s.OutputCount, s.LostCount, s.OverlapCount, s.StyleLoss, s.Encoding, s.MaxDeltaMS)
}
