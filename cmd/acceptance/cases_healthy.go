package main

import (
	"fmt"
	"math"
)

const (
	packetFrames  = 64
	segmentFrames = 48000
)

func checkHealthyThresholds(r *Report) error {
	if math.Abs(r.DeltaPct) > 0.1 {
		return fmt.Errorf("bitrate deviation %.4f%% exceeds 0.1%%", r.DeltaPct)
	}
	if r.SNRDB < 90 {
		return fmt.Errorf("SNR %.2f dB below 90 dB", r.SNRDB)
	}
	if r.AlignErrSamples > 1 {
		return fmt.Errorf("alignment error %d samples exceeds 1", r.AlignErrSamples)
	}
	if r.GapCount != 0 || r.Fragments == 0 {
		return fmt.Errorf("healthy delivery invalid: gaps=%d fragments=%d", r.GapCount, r.Fragments)
	}
	r.OK = true
	return nil
}

func caseNormal(out string) Report {
	dir := out
	f := genTone(toneSpec{SampleRate: 48000, Channels: 2, Frames: 96000, Frequency: 440})
	packets := packetize(f, 0, packetFrames)
	r, err := runHealthy(healthyOptions{
		caseName: "normal", trackID: "cam-normal", outDir: dir, fixture: f,
		arrivals: packets, bandwidthBPS: 4_000_000,
		packetFrames: packetFrames, segmentFrames: segmentFrames,
	})
	if err != nil {
		r.OK = false
		r.Error = err.Error()
		r.ErrorCode = "acceptance_failure"
		return r
	}
	if err := checkHealthyThresholds(&r); err != nil {
		r.OK = false
		r.Error = err.Error()
	}
	return r
}

func caseRecovery(out string) Report {
	dir := out
	f := genTone(toneSpec{SampleRate: 48000, Channels: 2, Frames: 96000, Frequency: 523.25})
	ordered := packetize(f, 0, packetFrames)
	shuffled := shuffledWithDuplicates(ordered)
	r, err := runHealthy(healthyOptions{
		caseName: "recovery", trackID: "cam-recovery", outDir: dir, fixture: f,
		arrivals: shuffled, bandwidthBPS: 2_000_000, // 80%=1.6M, selects 16-bit
		packetFrames: packetFrames, segmentFrames: segmentFrames,
	})
	if err != nil {
		r.OK = false
		r.Error = err.Error()
		r.ErrorCode = "acceptance_failure"
		return r
	}
	if d, _ := r.Details["duplicates"].(int); d < 2 {
		r.OK = false
		r.Error = fmt.Sprintf("recovery did not exercise duplicates: %v", r.Details["duplicates"])
		return r
	}
	if err := checkHealthyThresholds(&r); err != nil {
		r.OK = false
		r.Error = err.Error()
	}
	return r
}
