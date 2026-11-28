// Command avpipe is the terminal front-end for the reusable avpipe library.
// It only wires arguments to the library packages and renders their results;
// it contains no processing logic of its own.
package main

import (
	"flag"
	"fmt"
	"os"

	"avpipe/internal/fixture"
	"avpipe/internal/model"
	"avpipe/internal/stream"
)

// Exit codes from the acceptance contract.
const (
	exitOK         = 0
	exitVerifyFail = 1
	exitLocatable  = 2
	exitInterrupt  = 75
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitVerifyFail)
	}
	var err error
	switch os.Args[1] {
	case "fixture":
		err = cmdFixture(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	default:
		usage()
		err = model.NewError(model.CodeIO, false, "unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "avpipe: "+err.Error())
		if me, ok := model.AsError(err); ok {
			switch me.Code {
			case model.CodeManifest:
				os.Exit(exitVerifyFail)
			case model.CodeInfeasible:
				os.Exit(exitVerifyFail)
			default:
				os.Exit(exitLocatable)
			}
		}
		os.Exit(exitVerifyFail)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: avpipe <fixture|run|verify> [flags]")
}

func cmdFixture(args []string) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	caseName := fs.String("case", "normal", "fixture case: normal|damaged")
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return model.NewError(model.CodeIO, false, "--out is required")
	}
	desc, ok := fixture.Cases()[*caseName]
	if !ok {
		return model.NewError(model.CodeIO, false, "unknown case %q", *caseName)
	}
	if err := fixture.WriteCase(*out, desc); err != nil {
		return err
	}
	fmt.Printf("fixture=%s tracks=%d out=%s\n", desc.Name, len(desc.Tracks), *out)
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("input", "", "fixture input directory")
	maxKbps := fs.Int("max-kbps", 900, "maximum average bitrate in kbps")
	budgetMS := fs.Int("budget-ms", 80, "processing deadline in ms")
	stopAfter := fs.Int("stop-after", -1, "interrupt after processing sample N")
	resume := fs.Bool("resume", false, "resume from the last checkpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return model.NewError(model.CodeIO, false, "--input is required")
	}
	stateDir := *input + "/.avpipe"
	res, err := stream.Run(stream.RunRequest{
		InputDir: *input, StateDir: stateDir, MaxKbps: *maxKbps,
		BudgetMS: *budgetMS, TargetPermille: 800,
		StopAfter: *stopAfter, Resume: *resume,
	})
	if err != nil {
		return err
	}

	fmt.Printf("tracks=%d boundaries=%d repairable=%d\n",
		res.Tracks, res.Boundaries, res.Repairable)
	fmt.Print(res.Config.Explain())
	fmt.Printf("config_kbps=%d budget_ms=%d\n", res.Config.Kbps, res.Config.BudgetMS)
	if *resume {
		fmt.Printf("resume_from=%d\n", res.ResumeFrom)
	}
	for _, c := range res.Corrections {
		fmt.Printf("repair track=%d offset=%d kind=%s raw_ns=%d fixed_ns=%d shift_ns=%d source=%d\n",
			c.Track, c.Offset, c.Kind, c.RawTimeNS, c.FixedTimeNS, c.ShiftNS,
			c.Clock.SourceID)
	}
	for _, d := range res.Defects {
		kind := "fatal"
		if d.Recoverable {
			kind = "recoverable"
		}
		fmt.Printf("defect track=%d offset=%d code=%s %s detail=%q\n",
			d.Track, d.Offset, d.Code, kind, d.Message)
	}
	manifestWord := "none"
	if res.Manifest != nil {
		manifestWord = validWord(res.ManifestValid)
	}
	fmt.Printf("state=%s checkpoint_sample=%d duplicate=%d manifest_diff=%d manifest=%s\n",
		res.State, res.CheckpointSample, res.Duplicate, res.ManifestDiff,
		manifestWord)

	if res.State == model.StateStreaming {
		os.Exit(exitInterrupt)
	}
	if len(res.Defects) > 0 && res.Locatable {
		os.Exit(exitLocatable)
	}
	return nil
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "fixture input directory")
	maxKbps := fs.Int("max-kbps", 900, "bitrate cap used when re-deriving")
	budgetMS := fs.Int("budget-ms", 80, "deadline used when re-deriving")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return model.NewError(model.CodeIO, false, "--input is required")
	}
	v, err := stream.Verify(*input, *input+"/.avpipe", *maxKbps, *budgetMS, 800)
	if err != nil {
		return err
	}
	fmt.Printf("manifest=%s\n", validWord(v.Valid))
	for _, p := range v.Problems {
		fmt.Printf("mismatch %s\n", p)
	}
	if !v.Valid {
		return model.NewError(model.CodeManifest, false, "manifest verification failed")
	}
	return nil
}

func validWord(ok bool) string {
	if ok {
		return "valid"
	}
	return "invalid"
}
