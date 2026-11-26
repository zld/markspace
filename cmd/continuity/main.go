package main

import (
	"fmt"
	"os"
)

// Exit codes form the terminal contract used by scripts and demos.
const (
	exitOK                 = 0
	exitGeneric            = 1
	exitInputCorrupt       = 2
	exitConstraint         = 3
	exitCheckpointConflict = 4
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return exitOK
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "inspect":
		return runInspect(args[1:])
	case "plan":
		return runPlan(args[1:])
	case "resume":
		return runResume(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage(os.Stderr)
		return exitGeneric
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "continuity — local Matroska/WebM timeline and recovery tool")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  continuity demo --dir DIR --seed N --min-coverage-ppm PPM \\")
	fmt.Fprintln(w, "    --max-av-skew-ms MS --max-bitrate-bps BPS --budget-ms MS")
	fmt.Fprintln(w, "  continuity inspect --file PATH")
	fmt.Fprintln(w, "  continuity plan --dir FIXTURE_DIR [constraint overrides]")
	fmt.Fprintln(w, "  continuity resume --dir JOB_DIR --fixture-dir FIXTURE_DIR")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "exit codes: 0 ok/expected demo; 1 usage; 2 corrupt input; 3 infeasible constraints; 4 checkpoint conflict")
}
