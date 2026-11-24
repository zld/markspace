// media-check is the acceptance entry point. It runs entirely in the
// terminal: with -selftest it generates the sample set locally and checks
// every acceptance boundary; otherwise it processes the given MP4/MOV or
// RTP capture files as a batch.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediacheck/internal/adapter"
	mp4a "mediacheck/internal/adapter/mp4"
	"mediacheck/internal/selftest"
)

func main() {
	var (
		st         = flag.Bool("selftest", false, "generate samples and run the acceptance selftest")
		jitterMs   = flag.Int("jitter-ms", 80, "jitter buffer window in milliseconds")
		maxBufMB   = flag.Int("max-buffer-mb", 8, "maximum streaming buffer in MiB")
		targetKbps = flag.Float64("target-kbps", 5000, "target output bitrate in kbps")
		maxDelta   = flag.Float64("max-bitrate-delta", 0.05, "maximum relative bitrate deviation")
		keepDir    = flag.Bool("keep-dir", false, "preserve the selftest task directory and print its path")
	)
	flag.Parse()

	cfg := selftest.DefaultConfig(*jitterMs, int64(*maxBufMB)<<20, *targetKbps, *maxDelta)
	if *st {
		os.Exit(selftest.RunSelftest(cfg, os.Stdout, *keepDir))
	}
	files := flag.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: media-check -selftest [flags]")
		fmt.Fprintln(os.Stderr, "       media-check [flags] <file.mp4|file.mov|file.rtpcap>...")
		os.Exit(2)
	}
	os.Exit(runBatch(cfg, files))
}

// runBatch processes real input files. Exit code 1 when any input fails a
// boundary or contains unrecoverable media.
func runBatch(cfg selftest.Config, files []string) int {
	reg := adapter.NewRegistry()
	if err := reg.Register(mp4a.ParserV1{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	outDir, err := os.MkdirTemp("", "media-check-out-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(outDir)

	ok := true
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		fmt.Printf("== input %s ==\n", f)
		var res *selftest.Result
		if strings.HasSuffix(f, ".rtpcap") {
			out := &selftest.RTPOutcome{}
			res = selftest.ProcessRTP(context.Background(), cfg, name, f, outDir, out)
			selftest.PrintResult(os.Stdout, res, out)
			for _, r := range out.Ranges {
				if !r.Recoverable {
					ok = false // unrecoverable media fails the batch
				}
			}
		} else {
			res = selftest.ProcessMP4(context.Background(), cfg, reg, name, f, outDir)
			selftest.PrintResult(os.Stdout, res, nil)
		}
		if !res.OK {
			ok = false
		}
	}
	if !ok {
		return 1
	}
	return 0
}
