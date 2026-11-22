// Command mediatimeline demonstrates the deterministic media processing
// kernel. It generates synthetic MP4/PCM fixtures locally (no network, no
// external encoder) and runs three scenarios:
//
//	medialtimeline demo    --seed N --late-ms M --checkpoint-every K
//	medialtimeline corrupt  --kind truncated-stbl
//	medialtimeline resume   --kill-after 3s
//
// Exit codes: 0 success; 2 input damage located but recoverable; 3 an
// unrecoverable checkpoint failure.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"mediatimeline/internal/app"
	"mediatimeline/internal/clock"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := parseFlags(os.Args[2:])
	var code int
	var err error
	switch cmd {
	case "demo":
		code, err = runDemo(args)
	case "corrupt":
		code, err = runCorrupt(args)
	case "resume":
		code, err = runResume(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  mediatimeline demo    --seed <int> --late-ms <int> --checkpoint-every <int>
  mediatimeline corrupt --kind truncated-stbl|illegal-offset|bad-crc|checkpoint
  mediatimeline resume  --kill-after <duration>
`)
}

// flags is a minimal key/value flag set, sufficient for the fixed CLI surface.
type flags map[string]string

func parseFlags(argv []string) flags {
	f := flags{}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		key := strings.TrimPrefix(tok, "--")
		if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
			f[key] = argv[i+1]
			i++
		} else {
			f[key] = "true"
		}
	}
	return f
}

func (f flags) intOr(key string, def int) int {
	if v, ok := f[key]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func runDemo(f flags) (int, error) {
	opts := app.DemoOptions{
		Seed:            int64(f.intOr("seed", 42)),
		LateMS:          f.intOr("late-ms", 80),
		CheckpointEvery: f.intOr("checkpoint-every", 128),
	}
	m, err := app.RunDemo(workDir(), opts)
	if err != nil {
		return classify(err), err
	}
	printDemo(m)
	return 0, nil
}

func runCorrupt(f flags) (int, error) {
	kind := f["kind"]
	if kind == "" {
		kind = "truncated-stbl"
	}
	loc, err := app.RunCorrupt(workDir(), kind)
	if err != nil {
		return classify(err), err
	}
	fmt.Printf("error_offset=%d\ntrack_id=%d\nsample_index=%d\n",
		loc.Offset, loc.TrackID, loc.SampleIndex)
	return 2, nil
}

func runResume(f flags) (int, error) {
	d := 3 * time.Second
	if v, ok := f["kill-after"]; ok {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return 1, fmt.Errorf("invalid --kill-after: %w", err)
		}
		d = parsed
	}
	m, recoveryMS, replayed, err := app.RunResume(workDir(), d)
	if err != nil {
		return classify(err), err
	}
	fmt.Printf("recovery_ms=%d\nreplayed_events=%d\noutput_sha256=%s\n",
		recoveryMS, replayed, m.OutputSHA256)
	return 0, nil
}

// classify maps structured errors onto the documented process exit codes.
func classify(err error) int {
	if ce, ok := err.(*clock.Error); ok {
		if !ce.Recoverable() {
			return 3
		}
		return 2
	}
	return 1
}

func workDir() string {
	if d, err := os.MkdirTemp("", "mediatimeline-*"); err == nil {
		return d
	}
	return os.TempDir()
}

func printDemo(m app.Metrics) {
	fmt.Printf("event_count=%d\nwatermark_ns=%d\ndrift_ppm=%.3f\nresample_frames=%d\npeak_dbfs=%.3f\noutput_sha256=%s\n",
		m.EventCount, m.WatermarkNS, m.DriftPPM, m.ResampleFrames, m.PeakDBFS, m.OutputSHA256)
}
