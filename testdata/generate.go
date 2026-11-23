// Fixture generator entry point for the archivectl recovery pipeline.
// Run with: go run testdata/generate.go -out <dir> --seed N --loss-window W
// It writes only local synthetic EBML streams and an RTP capture; no
// external files are referenced.
//
//go:build ignore

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"archivectl/internal/fixture"
	"archivectl/internal/recover"
)

func main() {
	out := flag.String("out", "testdata/fixtures", "output directory")
	seed := flag.Int64("seed", 42, "deterministic RNG seed")
	lossWindow := flag.Int("loss-window", 8, "RTP loss window")
	flag.Parse()

	if *lossWindow <= 0 || *lossWindow > recover.MaxRecoveryWindow {
		fmt.Fprintf(os.Stderr, "loss-window must be in 1..%d\n",
			recover.MaxRecoveryWindow)
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	set := fixture.Generate(*seed, *lossWindow)

	writeFile(*out, fixture.NormalInterleave, set.Normal)
	writeFile(*out, fixture.UnknownLength, set.Unknown)
	writeFile(*out, fixture.TailTruncated, set.Truncated)
	writeFile(*out, fixture.CorruptCluster, set.Corrupt)
	writeFile(*out, fixture.IncompleteIndex, set.Incomplete)
	writeFile(*out, fixture.RTPCapture, set.RTP)

	manifest := fmt.Sprintf(
		"seed=%d\nloss_window=%d\nnormal=%d bytes\nunknown=%d bytes\ntruncated=%d bytes\ncorrupt=%d bytes\nincomplete_index=%d bytes\nrtp=%d packets\n",
		*seed, *lossWindow, len(set.Normal), len(set.Unknown),
		len(set.Truncated), len(set.Corrupt), len(set.Incomplete),
		set.RTPPackets)
	writeFile(*out, fixture.Manifest, []byte(manifest))
	fmt.Printf("fixtures written to %s (seed=%d)\n", *out, *seed)
}

func writeFile(dir, name string, data []byte) {
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "generate:", err)
	os.Exit(1)
}
