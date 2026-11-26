package acceptance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"mediarepair/internal/media"
)

const (
	ExitOK           = 0
	ExitUsage        = 2
	ExitInputOrWrite = 3
	ExitInterrupt    = 75
)

type Options struct {
	Scenario         string
	Root             string
	CheckpointFrames int
	AfterBlock       int
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ScenarioFunc func(Options, io.Writer) int

var scenarioRegistry = map[string]ScenarioFunc{}

func RegisterScenario(name string, fn ScenarioFunc) {
	scenarioRegistry[name] = fn
}

func Run(opts Options, out io.Writer) int {
	if opts.Scenario == "" {
		opts.Scenario = "all"
	}
	if opts.CheckpointFrames <= 0 {
		opts.CheckpointFrames = 4
	}
	if opts.AfterBlock <= 0 {
		opts.AfterBlock = 1
	}
	if opts.Root == "" {
		opts.Root = filepath.Join(".tmp", "acceptance")
	}
	if fn, ok := scenarioRegistry[opts.Scenario]; ok {
		return fn(opts, out)
	}
	fmt.Fprintf(out, "argument_error=unknown_scenario scenario=%q\n", opts.Scenario)
	return ExitUsage
}

func PrintError(out io.Writer, err error) {
	me := media.AsError(err)
	if me == nil {
		fmt.Fprintf(out, "error=%q\n", err.Error())
		return
	}
	fmt.Fprintf(out, "rejected=true code=%s stage=%s", me.Code, me.Stage)
	if me.File != "" {
		fmt.Fprintf(out, " file=%s", me.File)
	}
	if me.Frame > 0 {
		fmt.Fprintf(out, " frame=%d", me.Frame)
	}
	fmt.Fprintf(out, " offset=%d", me.Offset)
	if me.Err != nil {
		fmt.Fprintf(out, " reason=%q", me.Err.Error())
	}
	fmt.Fprintln(out)
}

func ExitForError(err error) int {
	if media.HasCode(err, media.CodeInterrupt) {
		return ExitInterrupt
	}
	if media.HasCode(err, media.CodeInvalidArgument) {
		return ExitUsage
	}
	return ExitInputOrWrite
}

func validSHA(s string) bool { return sha256Re.MatchString(s) }

func rawHash(fp string) string {
	for i := 0; i < len(fp); i++ {
		if fp[i] == ':' {
			return fp[i+1:]
		}
	}
	return fp
}

func resetRunState(root string) error {
	for _, p := range []string{
		filepath.Join(root, "work"),
		filepath.Join(root, "segments"),
		filepath.Join(root, "packages"),
	} {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func scenarioRoot(base, name string) string {
	return filepath.Join(base, name)
}
