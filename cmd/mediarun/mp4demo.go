package main

import (
	"context"
	"fmt"
	"path/filepath"

	"mediarun/internal/audio"
)

// demonstrateMP4 renders the normal fixture's combined WAV into an AAC MP4 and
// reads its timing/sample structure back through go-mp4, proving the reusable
// container path against a real file produced on this machine.
func demonstrateMP4(ctx context.Context, n *audio.Normalizer, outRoot string) error {
	wav := filepath.Join(outRoot, "normal", "work-normal", "work", "output.wav")
	mp4 := filepath.Join(outRoot, "normal", "output.m4a")
	if err := probeOutputMP4(ctx, n, wav, mp4); err != nil {
		return err
	}
	fmt.Printf("mp4_timeline: encoded and read %s via go-mp4 (samples/timescale ok)\n",
		filepath.Base(mp4))
	return nil
}
