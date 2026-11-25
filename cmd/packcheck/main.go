// Command packcheck is the acceptance adapter for the media processing
// kernel. It self-generates all fixtures (no external files), runs the full
// clock/subtitle/container/segment/stream/recovery/report pipeline and prints
// shell-checkable key=value conclusions.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type options struct {
	selfGenerate bool
	fpsToken     string
	slowLayer    string
	queueCap     int
	budgetBytes  int64
	corrupt      string
	truncate     map[string]bool
	splitAtCue   bool
	outDir       string
}

func parseArgs(args []string) (*options, error) {
	o := &options{
		fpsToken: "30000/1001-df", slowLayer: "low", queueCap: 2,
		budgetBytes: 0, truncate: map[string]bool{}, outDir: "",
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--self-generate":
			o.selfGenerate = true
		case "--fps":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.fpsToken = v
		case "--slow-layer":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.slowLayer = v
		case "--queue":
			v, err := next()
			if err != nil {
				return nil, err
			}
			if _, err := fmt.Sscanf(v, "%d", &o.queueCap); err != nil || o.queueCap <= 0 {
				return nil, fmt.Errorf("invalid --queue value %q", v)
			}
		case "--budget-bytes":
			v, err := next()
			if err != nil {
				return nil, err
			}
			if _, err := fmt.Sscanf(v, "%d", &o.budgetBytes); err != nil {
				return nil, fmt.Errorf("invalid --budget-bytes value %q", v)
			}
		case "--corrupt":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.corrupt = v
		case "--truncate":
			v, err := next()
			if err != nil {
				return nil, err
			}
			kv := strings.SplitN(v, "=", 2)
			if len(kv) != 2 || kv[0] != "layer" {
				return nil, fmt.Errorf("invalid --truncate spec %q (want layer=NAME)", v)
			}
			o.truncate[kv[1]] = true
		case "--split-at-cue":
			o.splitAtCue = true
		case "--out":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.outDir = v
		case "-h", "--help":
			usage(os.Stdout)
			os.Exit(0)
		default:
			return nil, fmt.Errorf("unknown flag %q", arg)
		}
	}
	if !o.selfGenerate {
		return nil, fmt.Errorf("--self-generate is required (no external inputs are supported)")
	}
	if o.outDir != "" {
		if err := rejectPathTraversal(o.outDir); err != nil {
			return nil, err
		}
	}
	return o, nil
}

func rejectPathTraversal(p string) error {
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) ||
		filepath.IsAbs(clean) && strings.Contains(clean, "..") {
		return fmt.Errorf("path traversal rejected: %q", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("path traversal rejected: %q", p)
	}
	return nil
}

func usage(w *os.File) {
	fmt.Fprintln(w, "packcheck --self-generate [--fps TOKEN] [--slow-layer NAME]",
		"[--queue N] [--budget-bytes N] [--corrupt MODE] [--truncate layer=NAME]",
		"[--split-at-cue] [--out DIR]")
}

// layerSpec is the synthetic content of one representation.
type layerSpec struct {
	name        string
	timescale   uint32
	samples     uint32
	segFrames   uint32
	rtpStep     uint32
	packetCount int
}

func layerSpecs() []layerSpec {
	// low/mid/high representations share one 90kHz clock and frame grid.
	return []layerSpec{
		{name: "low", timescale: 25, samples: 100, segFrames: 25, rtpStep: 3600, packetCount: 100},
		{name: "mid", timescale: 25, samples: 100, segFrames: 25, rtpStep: 3600, packetCount: 100},
		{name: "high", timescale: 25, samples: 100, segFrames: 25, rtpStep: 3600, packetCount: 100},
	}
}

// slowTimeout is the deadline a never-draining layer may block one Send.
const slowTimeout = 25 * time.Millisecond

func main() {
	os.Exit(run(os.Args[1:]))
}
