package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediakernel/internal/gen"
)

// demoSpecs describes the two synthetic tracks of the demo container:
// a video-like track (90 kHz, 30 fps) and an audio-like track (48 kHz).
func demoSpecs(samples, size int) []gen.TrackSpec {
	v := make([]uint32, samples)
	for i := range v {
		v[i] = uint32(size + (i*37)%500)
	}
	a := make([]uint32, samples/2)
	for i := range a {
		a[i] = uint32(180 + (i*13)%40)
	}
	return []gen.TrackSpec{
		{ID: 1, Timescale: 90000, SampleDelta: 3000, SamplesPerChunk: 8, SampleSizes: v},
		{ID: 2, Timescale: 48000, SampleDelta: 1024, SamplesPerChunk: 8, SampleSizes: a},
	}
}

func cmdGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	dir := fs.String("dir", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("generate: --dir is required")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}

	good := gen.BuildMP4(demoSpecs(300, 1800)) // ~600 KB
	if err := os.WriteFile(filepath.Join(*dir, "good.mp4"), good, 0o644); err != nil {
		return err
	}

	// Truncated inside moov: the first unreadable box header is the error.
	cut := 16 + 96 // ftyp (16) + 96 bytes into moov
	if err := os.WriteFile(filepath.Join(*dir, "truncated.mp4"), good[:cut], 0o644); err != nil {
		return err
	}

	trace, err := gen.BuildTrace(good, gen.TraceOptions{
		ChunkSize: 1200, DupEvery: 7, LossStart: 200, LossLen: 6,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dir, "reordered.rtp"), trace, 0o644); err != nil {
		return err
	}

	// Interrupted-run fixture: a larger input so resume timing is measurable.
	big := gen.BuildMP4(demoSpecs(4000, 1900)) // ~8 MB
	bigTrace, err := gen.BuildTrace(big, gen.TraceOptions{
		ChunkSize: 1400, DupEvery: 11, LossStart: 3000, LossLen: 9,
	})
	if err != nil {
		return err
	}
	idir := filepath.Join(*dir, "interrupted")
	if err := os.MkdirAll(idir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(idir, "input.rtp"), bigTrace, 0o644); err != nil {
		return err
	}

	fmt.Printf("generated dir=%s good_bytes=%d trace_bytes=%d interrupted_bytes=%d\n",
		*dir, len(good), len(trace), len(bigTrace))
	return nil
}
