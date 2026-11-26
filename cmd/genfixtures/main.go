// Command genfixtures writes deterministic local MP4/RTP fault samples.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"assetpack/internal/samplegen"
)

func main() {
	out := flag.String("out", filepath.Join(os.TempDir(), "assetpack-fixtures"), "fixture output directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "fixture generation failed:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{}
	for _, seq := range []int{1, 2} {
		b, err := samplegen.BuildContainer(seq, false)
		if err != nil {
			return err
		}
		files[fmt.Sprintf("sequence-%d.mp4", seq)] = b.Data
	}
	fmp4, err := samplegen.BuildContainer(1, true)
	if err != nil {
		return err
	}
	files["sequence-1.fmp4.mp4"] = fmp4.Data

	valid, err := samplegen.BuildRTPStream("")
	if err != nil {
		return err
	}
	files["rtp-valid.bin"] = valid
	for _, fault := range []string{"rtp-duplicate", "rtp-missing"} {
		b, err := samplegen.BuildRTPStream(fault)
		if err != nil {
			return err
		}
		files[fault+".bin"] = b
	}
	seq1, err := samplegen.BuildContainer(2, false)
	if err != nil {
		return err
	}
	files["bad-mdat.mp4"] = samplegen.TruncateMDAT(seq1.Data, 128)
	unknown, err := samplegen.UnknownVersionContainer()
	if err != nil {
		return err
	}
	files["unknown-adapter-version.mp4"] = unknown

	for name, data := range files {
		path := filepath.Join(out, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("fixture=%s bytes=%d\n", path, len(data))
	}
	return nil
}
