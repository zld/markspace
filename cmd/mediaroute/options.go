package main

import (
	"fmt"
	"path/filepath"

	"mediaroute/internal/journal"
	"mediaroute/internal/pipeline"
)

// defaultJournalDir keeps all generated state inside the working tree.
func defaultJournalDir(seed int64) string {
	return filepath.Join(".mediaroute", fmt.Sprintf("seed-%d", seed))
}

// crashPoints are the crash injection points the demo understands. An empty
// value means "no crash".
var crashPoints = map[string]bool{
	"":                         true,
	journal.PointIndexCommit:   true,
	journal.PointSegmentCommit: true,
	journal.PointAfterWALBegin: true,
}

// allowedFlags is the exact flag set each subcommand accepts. Unknown flags
// are rejected as argument errors instead of being silently ignored.
var allowedFlags = map[string]map[string]bool{
	"demo": {
		"seed": true, "loss-burst": true, "reorder-window": true,
		"crash-at": true, "corrupt-index": true, "corrupt-stream": true,
		"journal-dir": true,
	},
	"verify": {
		"seed": true, "loss-burst": true, "reorder-window": true,
		"journal-dir": true, "repeat": true,
	},
}

func buildOptions(sub string, flags map[string]string) (pipeline.Options, error) {
	allowed := allowedFlags[sub]
	for name := range flags {
		if !allowed[name] {
			return pipeline.Options{}, argError("unsupported flag --" + name)
		}
	}
	seed, err := atoiFlag(flags, "seed", 41)
	if err != nil {
		return pipeline.Options{}, err
	}
	burst, err := atoiFlag(flags, "loss-burst", 3)
	if err != nil {
		return pipeline.Options{}, err
	}
	window, err := atoiFlag(flags, "reorder-window", 8)
	if err != nil {
		return pipeline.Options{}, err
	}
	dir, ok := flags["journal-dir"]
	if !ok || dir == "" {
		dir = defaultJournalDir(int64(seed))
	}
	if window < 1 || burst < 0 {
		return pipeline.Options{}, argError("reorder-window must be >= 1 and loss-burst >= 0")
	}
	crash := flags["crash-at"]
	if !crashPoints[crash] {
		return pipeline.Options{}, argError("unknown crash point " + crash +
			" (want index-commit, segment-commit or wal-begin)")
	}
	return pipeline.Options{
		Seed:          int64(seed),
		LossBurst:     burst,
		ReorderWindow: window,
		CrashAt:       crash,
		JournalDir:    dir,
		CorruptIndex:  flags["corrupt-index"] == "true",
		CorruptStream: flags["corrupt-stream"] == "true",
	}, nil
}

// errBadArguments is the sentinel the CLI maps to exit code 4.
var errBadArguments = argError("invalid argument")

type argError string

func (e argError) Error() string { return string(e) }
