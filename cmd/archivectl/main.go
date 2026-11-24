// archivectl is the acceptance entry point for the archival recovery
// pipeline. It is terminal-only: every function is driven by flags and
// reports to stdout.
//
//	archivectl demo   --out DIR [--corrupt-at N] [--truncate-at N] [--interrupt-after K]
//	archivectl resume --dir DIR
//
// Exit codes: 0 = clean success, 2 = input damaged but partial recovery
// completed, 1 = parameter or verification failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"archive/internal/sample"
	"archive/internal/scan"
)

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

func dispatch(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:])
	case "resume":
		return cmdResume(args[1:])
	default:
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  archivectl demo   --out DIR [--corrupt-at N] [--truncate-at N] [--interrupt-after K]
  archivectl resume --dir DIR
exit codes: 0 clean, 2 damaged input partially recovered, 1 parameter/verify failure`)
}

func cmdDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (must be empty or new)")
	corruptAt := fs.Int64("corrupt-at", -1, "flip one byte at this offset in the MP4 fixture")
	truncateAt := fs.Int64("truncate-at", -1, "truncate the MP4 fixture at this offset")
	interruptAfter := fs.Int("interrupt-after", -1, "stop after K committed packets and save a checkpoint")
	if fs.Parse(args) != nil || *out == "" {
		usage()
		return 1
	}
	if dirNonEmpty(*out) {
		fmt.Fprintf(os.Stderr, "error: %s exists and is not empty; refusing to overwrite\n", *out)
		return 1
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	// Fixtures are generated, never fetched: damaged+truncated MP4 as the
	// pipeline input, a clean MXF for the scanner, an incomplete index.
	mp4Path := filepath.Join(*out, "input.mp4")
	info, err := sample.GenMP4(mp4Path, sample.MP4Options{
		CorruptAt: *corruptAt, TruncateAt: *truncateAt, ColorConflict: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: generate MP4 fixture:", err)
		return 1
	}
	mxfPath := filepath.Join(*out, "normal.mxf")
	if _, err := sample.GenMXF(mxfPath, sample.MXFOptions{CorruptAt: -1, TruncateAt: -1}); err != nil {
		fmt.Fprintln(os.Stderr, "error: generate MXF fixture:", err)
		return 1
	}
	// An incomplete index on purpose: the pipeline must fall back to scan.
	if err := os.WriteFile(filepath.Join(*out, "index.json"), []byte(`{"inputSHA256": "unfin`), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	meta := &runMeta{
		CorruptAt:          *corruptAt,
		TruncateAt:         *truncateAt,
		CorruptPacketSize:  info.PacketSizeAt(*corruptAt),
		TruncatePacketSize: info.PacketSizeAt(*truncateAt),
	}
	if err := saveMeta(*out, meta); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("fixtures: input.mp4 (corrupt@%d, truncate@%d), normal.mxf, incomplete index\n",
		*corruptAt, *truncateAt)
	printMXFSummary(mxfPath)
	return runPipeline(*out, *interruptAfter)
}

func cmdResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	dir := fs.String("dir", "", "working directory of a previous demo run")
	if fs.Parse(args) != nil || *dir == "" {
		usage()
		return 1
	}
	return runPipeline(*dir, -1)
}

func dirNonEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func printMXFSummary(path string) {
	res, err := scan.ScanMXF(path)
	if err != nil {
		fmt.Println("mxf scan failed:", err)
		return
	}
	fmt.Printf("mxf scan: %s partitions=%v tracks=%d duration=%.1fms errors=%d\n",
		filepath.Base(path), res.Partitions, len(res.Tracks),
		float64(res.DurationNs)/1e6, len(res.Errors))
	for _, t := range res.Tracks {
		fmt.Printf("  track #%d %s timescale=%d samples=%d\n", t.ID, t.Type, t.Timescale, len(t.Samples))
	}
}
