// Command mediapack drives the reusable media-processing kernel from the
// terminal. It generates deterministic WebM samples (including controlled
// corruptions), runs resumable batch processing, and prints numeric
// acceptance results: block counts, the stable track mapping and payload
// SHA-256 digests.
//
// Exit codes: 0 success, 2 corrupted/unprocessable input, 3 corrupt index.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"mediapack/internal/batch"
	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/fixture"
	"mediapack/internal/rtp"
	"mediapack/internal/verify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "run":
		err = runCmd(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		code := ExitCode(err)
		fmt.Fprintf(os.Stderr, "ERROR (exit %d): %v\n", code, err)
		os.Exit(code)
	}
}

// ExitCode maps a structured fault onto the documented process exit code:
// 3 for a corrupt checkpoint index, 2 for any other processing failure.
func ExitCode(err error) int {
	if fault.Is(err, fault.KindCheckpointCorrupt) {
		return 3
	}
	return 2
}

func usage() {
	fmt.Fprintln(os.Stderr, `mediapack - local WebM processing kernel

usage:
  mediapack demo --seed N --out DIR
      generates valid and corrupted WebM samples and prints block counts,
      track mapping and payload SHA-256.
  mediapack run --input FILE --index DIR [--interrupt-after N] [--corrupt-index]
      processes one input with checkpointed, resumable batching.`)
}

func parseFlags(args []string) map[string]string {
	flags := make(map[string]string)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 2 && a[:2] == "--" {
			key := a[2:]
			if i+1 < len(args) && len(args[i+1]) > 0 && args[i+1][0] != '-' {
				flags[key] = args[i+1]
				i++
			} else {
				flags[key] = "true"
			}
		}
	}
	return flags
}

// runDemo generates the full sample set and prints the digest for each.
func runDemo(args []string) error {
	flags := parseFlags(args)
	seed := int64(7)
	if v, ok := flags["seed"]; ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid --seed: %w", err)
		}
		seed = n
	}
	outDir, ok := flags["out"]
	if !ok {
		return fmt.Errorf("--out directory is required")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	spec := fixture.Spec{
		Seed:     seed,
		Codecs:   []string{"V_VP9", "A_OPUS", "S_TEXT/WEBVTT"},
		Clusters: 4, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 64,
	}
	specs := []struct {
		name string
		corr fixture.CorruptKind
		unk  bool
	}{
		{"valid.webm", fixture.CorruptNone, false},
		{"unknown-length.webm", fixture.CorruptNone, true},
		{"truncated.webm", fixture.CorruptTruncate, false},
		{"oversize.webm", fixture.CorruptBlockOversize, false},
		{"regression.webm", fixture.CorruptTimestampRegression, false},
	}
	// Each sample is generated and classified. A successful generation
	// returns 0; the per-sample status below records which inputs the
	// kernel rejects, and the `run` command maps those rejections onto
	// exit codes 2 (bad input) and 3 (bad index).
	statuses := make(map[string]string)
	for _, sc := range specs {
		s := spec
		s.UnknownLength = sc.unk
		raw, detail := fixture.Build(s, sc.corr)
		path := filepath.Join(outDir, sc.name)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return err
		}
		fmt.Printf("=== %s (bytes=%d) ===\n", sc.name, len(raw))
		if err := printStreamReport(raw); err != nil {
			// Corrupted samples are expected to be rejected; report the
			// exact fault location and classify the status.
			fmt.Printf("status=rejected fault: %v\n", err)
			statuses[sc.name] = "rejected"
		} else {
			fmt.Printf("status=ok\n")
			statuses[sc.name] = "ok"
		}
		if detail.CorruptOffset >= 0 {
			fmt.Printf("expected_corrupt_offset=%d\n", detail.CorruptOffset)
		}
	}
	if err := demoRTPReport(outDir); err != nil {
		return err
	}
	fmt.Print("sample_status:\n")
	for _, sc := range specs {
		fmt.Printf("  %s=%s\n", sc.name, statuses[sc.name])
	}
	return nil
}

// printStreamReport opens a stream and prints the deterministic digest report.
func printStreamReport(raw []byte) error {
	d, err := demux.Open(newReadSeeker(raw))
	if err != nil {
		return err
	}
	acc := verify.NewAccumulator(d.Mapping())
	if err := d.Stream(func(p *demux.Packet) error {
		acc.Add(p)
		return nil
	}); err != nil {
		return err
	}
	rep := acc.Finalize(nil)
	fmt.Print(rep.Render())
	return nil
}

// demoRTPReport demonstrates sequence-anomaly detection on synthetic packets.
func demoRTPReport(outDir string) error {
	// Build a short continuous stream then introduce a jump, verifying the
	// verifier reports the sequence and associated media offset.
	v := rtp.NewVerifier()
	passed := 0
	for seq := uint16(0); seq < 5; seq++ {
		dg := rtpDatagram(seq, []byte{byte(seq)})
		if _, err := v.Check(dg, int64(seq)*16, 1); err != nil {
			return err
		}
		passed++
	}
	// A jump (expected 5, got 8) must be reported.
	if _, err := v.Check(rtpDatagram(8, []byte("x")), 80, 1); err == nil {
		return fmt.Errorf("RTP jump was not detected")
	} else {
		fmt.Printf("rtp: continuous_packets=%d detected_anomaly=%v\n", passed, err)
	}
	return nil
}

func runCmd(args []string) error {
	flags := parseFlags(args)
	input, ok := flags["input"]
	if !ok {
		return fmt.Errorf("--input is required")
	}
	index, ok := flags["index"]
	if !ok {
		return fmt.Errorf("--index is required")
	}
	interrupt := 0
	if v, ok := flags["interrupt-after"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid --interrupt-after: %w", err)
		}
		interrupt = n
	}
	_, corruptIndex := flags["corrupt-index"]

	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	in := batch.Input(raw)
	runner := batch.New(batch.Options{
		InputPath:      input,
		IndexDir:       index,
		BatchSize:      3,
		InterruptAfter: interrupt,
		CorruptIndex:   corruptIndex,
	})
	var out bytes.Buffer
	res, err := runner.Execute(in, &out)
	if err != nil {
		return err
	}
	if res.Resume.Resumed {
		fmt.Printf("resume_block=%d checkpoint_offset=%d\n",
			res.Resume.ResumeBlock, res.Resume.CheckpointOff)
		fmt.Printf("resume_elapsed_ms=%d\n", res.ElapsedMS)
	}
	if res.Report != nil {
		fmt.Print(res.Report.Render())
	}
	return nil
}
