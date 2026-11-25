package main

import (
	"fmt"

	"mediasync/internal/pipeline"
)

// printSummary writes every acceptance-required metric to stdout in a stable
// order: packet counts, recovery gaps, buffer levels, frame/audio hashes,
// pixel statistics, peak loudness and the maximum alignment error.
func printSummary(res *pipeline.Result) {
	s := res.Summary
	fmt.Println("== packet recovery ==")
	fmt.Printf("total=%d emitted=%d corrupt=%d duplicates=%d late=%d missing=%d\n",
		s.Packets.TotalPackets, s.Packets.Emitted, s.Packets.Corrupt,
		s.Packets.Duplicates, s.Packets.Late, s.Packets.Missing)
	fmt.Printf("buffer high-water=%d jitter target=%d level=[%d,%d] smoothed_bps=%d\n",
		s.Packets.HighWater, s.Packets.JitterTarget,
		s.Packets.JitterMin, s.Packets.JitterMax, s.Packets.SmoothedBPS)
	fmt.Printf("recovery gaps=%d\n", len(s.Packets.Gaps))
	for _, g := range s.Packets.Gaps {
		fmt.Printf("  gap seq=[%d,%d] count=%d\n", g.Start, g.End, g.Count)
	}

	fmt.Println("== alignment ==")
	fmt.Printf("cues=%d drift_samples=%d max_audio_video_error=%d ms (%d ticks)\n",
		s.Alignment.CueCount, s.Alignment.DriftSamples,
		s.Alignment.MaxDriftMs, s.Alignment.MaxDriftTick)

	fmt.Println("== video frames ==")
	for _, f := range s.Frames {
		fmt.Printf("frame[%03d] sha256=%s pixels[min=%d max=%d mean=%.4f std=%.4f] align_err=%d ticks\n",
			f.Index, f.SHA256, f.PixelMin, f.PixelMax, f.PixelMean, f.PixelStd, f.AlignError)
	}

	fmt.Println("== audio fragments ==")
	for _, a := range s.Audio {
		fmt.Printf("audio[%02d] sha256=%s peak=%d peak_loudness=%.4f dBFS align_err=%d ticks\n",
			a.Index, a.SHA256, a.PeakAmplitude, a.PeakLoudness, a.AlignError)
	}

	fmt.Println("== committed stages ==")
	for _, st := range res.Stages {
		fmt.Printf("stage=%s artifacts=%d\n", st.Stage, len(st.Manifest.Artifacts))
		for _, art := range st.Manifest.Artifacts {
			fmt.Printf("  %s size=%d sha256=%s\n", art.Path, art.Size, art.SHA256)
		}
	}
}
