// Command tracecheck is the offline regression entry point for weak-network
// RTP traces stored in Matroska/WebM. It is terminal-only, works fully
// offline on local files, and never executes input content.
//
// Exit codes:
//
//	0 - every rule passed
//	1 - business threshold not met (e.g. alignment skew, failed segments)
//	2 - corrupt input or invalid arguments
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	exitOK       = 0
	exitBusiness = 1
	exitInput    = 2
)

type options struct {
	generate       bool
	outDir         string
	input          string
	bitratesCSV    string
	segmentMS      int
	maxSkewMS      int
	queue          int
	slowConsumerMS int
	cacheDir       string
	resumeDir      string
	selfTest       bool
}

func parseFlags(args []string) (options, error) {
	fs := flag.NewFlagSet("tracecheck", flag.ContinueOnError)
	var o options
	fs.BoolVar(&o.generate, "generate", false, "generate fixture files into --out")
	fs.StringVar(&o.outDir, "out", "", "output directory for generated fixtures")
	fs.StringVar(&o.input, "input", "", "input .mkv/.webm file")
	fs.StringVar(&o.bitratesCSV, "bitrates", "16000,32000", "comma-separated representation bitrates")
	fs.IntVar(&o.segmentMS, "segment-ms", 1000, "segment duration in milliseconds")
	fs.IntVar(&o.maxSkewMS, "max-skew-ms", 50, "alignment skew threshold in milliseconds")
	fs.IntVar(&o.queue, "queue", 4, "bounded queue capacity (items)")
	fs.IntVar(&o.slowConsumerMS, "slow-consumer-ms", 0, "artificial per-item consumer delay in ms")
	fs.StringVar(&o.cacheDir, "cache-dir", "", "content-addressable cache directory")
	fs.StringVar(&o.resumeDir, "resume-dir", "", "checkpoint/resume directory")
	fs.BoolVar(&o.selfTest, "self-test-cases", false, "run the built-in corruption and recovery suite")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	return o, nil
}

func parseBitrates(csv string) ([]uint32, error) {
	var out []uint32
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid bitrate %q: %w", part, err)
		}
		out = append(out, uint32(v))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one bitrate is required")
	}
	return out, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	o, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return exitInput
	}
	switch {
	case o.generate:
		return cmdGenerate(o)
	case o.selfTest:
		return cmdSelfTest(o)
	case o.input != "":
		return cmdAnalyze(o)
	default:
		fmt.Fprintln(os.Stderr, "nothing to do: pass --generate, --self-test-cases or --input")
		return exitInput
	}
}

// dirs resolves the cache and resume directories, creating defaults under
// the resume directory when not explicitly supplied.
func dirs(o options) (cacheDir, resumeDir string, err error) {
	resumeDir = o.resumeDir
	if resumeDir == "" {
		resumeDir = filepath.Join(os.TempDir(), "tracecheck-resume")
	}
	cacheDir = o.cacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(resumeDir, "cache")
	}
	if err := os.MkdirAll(resumeDir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", "", err
	}
	return cacheDir, resumeDir, nil
}
