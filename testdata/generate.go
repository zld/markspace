// Standalone fixture generator. The reusable, importable generator lives in
// internal/fixture (Go ignores directories named testdata for imports); this
// program renders one deterministic fixture set to disk for offline review.
//
// Run with: go run ./testdata/generate --seed 42 --out ./generated
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	fx "mediakernel/internal/fixture"
)

func main() {
	seed := flag.Int64("seed", 42, "deterministic RNG seed")
	burst := flag.Int("loss-burst", 8, "synthetic burst length")
	out := flag.String("out", "generated", "output directory")
	flag.Parse()

	if err := run(*seed, *burst, *out); err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}

func run(seed int64, burst int, out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	f := fx.NewGenerator(seed).Build(burst, []int{900, 2500, 700})
	files := map[string]string{
		"manifest.mpd":  f.DASHMPD,
		"playlist.m3u8": f.HLSPlaylist,
		"damaged.m3u8":  f.HLSDamaged,
	}
	for name, content := range files {
		path := filepath.Join(out, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("wrote %d manifest files and %d fault cases to %s (seed=%d)\n",
		len(files), len(f.Cases), out, seed)
	return nil
}
