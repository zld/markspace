package verify

import (
	"math"
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/manifest"
	"mediacore/internal/scan"
)

// VerifySamples compares rescanned output bytes against recovered samples.
func VerifySamples(outputPath string, mappings []scan.TrackMapping, expected []scan.SampleRef, targetBitrate int64) (*Result, error) {
	out, err := scan.BuildIndex(outputPath, scan.DefaultParams())
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), outputPath, -1)
	}
	expectedBySource := groupSamples(expected)
	actualByOutput := groupSamples(out.Samples)

	var audioDiffs int64
	videoExact, comparedVideo := true, false
	for _, mp := range mappings {
		want := expectedBySource[mp.SourceTrackID]
		got := actualByOutput[mp.OutputTrackID]
		if len(got) != len(want) {
			return nil, fault.New(fault.Verification, "recovered sample count differs in output")
		}
		for i := range want {
			w := want[i].Data
			g := sliceSample(raw, got[i])
			if string(w) != string(g) {
				if mp.Kind == "audio" {
					audioDiffs += countAudioSampleDiffs(w, g)
				} else {
					videoExact = false
				}
			}
		}
		if mp.Kind == "video" && len(want) > 0 {
			comparedVideo = true
		}
	}
	if !videoExact || audioDiffs != 0 {
		return nil, fault.New(fault.Verification, "recovered payload is not lossless")
	}

	durationNS := maxDuration(out.Tracks)
	if durationNS <= 0 {
		return nil, fault.New(fault.Verification, "RTP output duration is zero")
	}
	st, _ := os.Stat(outputPath)
	actual := float64(st.Size()) * 8 / (float64(durationNS) / float64(clock.NSPerSecond))
	target := targetBitrate
	if target <= 0 {
		target = int64(math.Round(actual))
	}
	delta := math.Abs(actual-float64(target)) / float64(target) * 100
	align, err := alignment(out)
	if err != nil {
		return nil, err
	}
	psnr, value := "+Inf/exact", math.Inf(1)
	if !comparedVideo {
		psnr, value = "n/a", math.NaN()
	}
	metrics := manifest.Metrics{
		TargetBitrate: target, ActualBitrate: round2(actual), BitrateDeltaPct: round4(delta),
		VideoPSNR: psnr, VideoPSNRValue: value, AudioDiffSamples: audioDiffs,
		ManifestAlignmentMS: round4(align),
	}
	if delta > 1 || math.Abs(align) > 1 {
		return nil, fault.New(fault.Verification, "RTP output metrics outside tolerance")
	}
	return &Result{Metrics: metrics, OutputIndex: out}, nil
}
