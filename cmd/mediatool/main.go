// Command mediatool drives the media processing kernel from a terminal.
//
//	mediatool demo   generate a capture, process it, interrupt, resume, verify
//	mediatool scan   scan a capture and print located damage
//
// Exit codes: 0 success, 2 input damaged but precisely located, 3 unrecoverable
// metadata or alignment error, 4 checkpoint/input digest mismatch.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediatool/internal/fault"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(3)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "scan":
		err = runScan(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(3)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(fault.ExitCode(err))
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediatool - RTP/RTCP media processing kernel

Usage:
  mediatool demo [flags]   full pipeline: generate, scan, segment, resume, verify
  mediatool scan [flags]   scan a generated capture and print located damage

Demo flags:
  --seed int              fixture seed (default 42)
  --tracks int            track count (default 4)
  --bitrates int          representation ladder depth (default 3)
  --inject-loss int       packets to drop (default 0)
  --inject-truncation int datagrams to truncate (default 0)
  --interrupt-after int   stop after N committed segments, then resume (default 0)
  --workdir string        output directory (default: a new temp dir)
  --keep                  keep the work directory and print its path

Scan flags mirror the demo injection flags.
`)
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "fixture seed")
	tracks := fs.Int("tracks", 4, "track count")
	bitrates := fs.Int("bitrates", 3, "representation ladder depth")
	injectLoss := fs.Int("inject-loss", 0, "packets to drop")
	injectTrunc := fs.Int("inject-truncation", 0, "datagrams to truncate")
	interruptAfter := fs.Int("interrupt-after", 0, "stop after N committed segments")
	workDir := fs.String("workdir", "", "output directory")
	keep := fs.Bool("keep", false, "keep the work directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateDemoFlags(*tracks, *bitrates, *injectLoss, *injectTrunc, *interruptAfter); err != nil {
		return err
	}

	dir := *workDir
	if dir == "" {
		d, err := os.MkdirTemp("", "mediatool-demo-*")
		if err != nil {
			return err
		}
		dir = d
	}
	defer func() {
		if !*keep && *workDir == "" {
			os.RemoveAll(dir)
		}
	}()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	res, err := demo(*seed, *tracks, *bitrates, *injectLoss, *injectTrunc, *interruptAfter, dir)
	if err != nil {
		return err
	}
	res.workDir = dir
	printDemo(res)
	if *keep || *workDir != "" {
		fmt.Printf("workdir=%s\n", dir)
	}

	// Injected input damage was located and recovered from: exit 2. Metadata
	// or alignment failures propagate as 3, a refused checkpoint as 4.
	if res.corruptions > 0 || res.lossPackets > 0 {
		return &fault.Fault{Kind: fault.KindLoss, Offset: res.damageOffset,
			Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged,
			Detail: "input contained located damage; output recovered"}
	}
	return nil
}

func validateDemoFlags(tracks, bitrates, loss, trunc, interrupt int) error {
	if tracks < 1 || tracks > 8 {
		return fmt.Errorf("--tracks must be between 1 and 8")
	}
	if bitrates < 1 || bitrates > 8 {
		return fmt.Errorf("--bitrates must be between 1 and 8")
	}
	if loss < 0 || trunc < 0 || interrupt < 0 {
		return fmt.Errorf("injection and interrupt counts must be non-negative")
	}
	return nil
}

func printDemo(r demoResult) {
	fmt.Printf("tracks=%d\n", r.tracks)
	fmt.Printf("representations=%d\n", r.representations)
	fmt.Printf("loss_packets=%d\n", r.lossPackets)
	fmt.Printf("corruptions=%d\n", r.corruptions)
	fmt.Printf("damage_offset_bytes=%d\n", r.damageOffset)
	fmt.Printf("offset_error_bytes=%d\n", r.offsetError)
	fmt.Printf("resume_ms=%d\n", r.resumeMs)
	fmt.Printf("manifest_segments=%d\n", r.manifestSegs)
	fmt.Printf("alignment_skew_ms=%d\n", r.alignmentSkew)
	fmt.Printf("output_sha256=%s\n", r.outputSHA)
}
