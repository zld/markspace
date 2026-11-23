package pipeline

import (
	"path/filepath"

	"mediapipe/internal/fault"
	"mediapipe/internal/testdata"
)

// fixturePaths collects the generated input files for one demo run.
type fixturePaths struct {
	mp4        string
	mxf        string
	rtp        string
}

// buildFixtures generates deterministic MP4/MXF/RTP material and applies the
// requested corruption. All files stay inside the run working directory.
func buildFixtures(opts Options, errs *fault.Batch) fixturePaths {
	dir := filepath.Join(opts.WorkDir, "input")
	durationNanos := int64(4_000_000_000)
	audio := testdata.NewAudio(opts.Seed, 48000, durationNanos)
	frameCount := 90
	// Frame payload sizes are derived from the bandwidth policy target so the
	// measured bitrate converges to target_kbps rather than to an arbitrary
	// luma-plane size.
	targetKbps := opts.TargetKbps
	if targetKbps <= 0 {
		targetKbps = 900
	}
	frames := testdata.NewBitrateFrames(opts.Seed, frameCount, 64, 48, 12,
		targetKbps, 30)
	r := testdata.NewRand(opts.Seed)

	dupIdx := -1
	if opts.Corruption == testdata.CorruptVFRDupPTS {
		dupIdx = 7
	}
	pts, dts, dur := testdata.VFRTiming(r, frameCount,
		1_000_000_000/30, 500_000_000, dupIdx)
	if !opts.VFR {
		dur = testdata.CFRDurations(frameCount, 1_000_000_000/30)
		pts, dts = nil, nil
		for i := range frames {
			base := int64(500_000_000) + int64(i)*1_000_000_000/30
			pts = append(pts, base)
			dts = append(dts, base)
		}
	}

	colors := []testdata.ColorSpec{{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true}}
	if opts.Corruption == testdata.CorruptConflictMeta {
		colors = append(colors, testdata.ColorSpec{
			Primaries: 1, Transfer: 1, Matrix: 1, FullRange: false})
	}
	mp4Spec := testdata.MP4Spec{
		Seed: opts.Seed, Width: 64, Height: 48, Timescale: 30000,
		Audio: audio, Frames: frames, VideoPTS: pts, VideoDTS: dts,
		VideoDur: dur, StartNanos: 500_000_000, Rotation: 90,
		Crop: [4]int64{2, 2, 2, 2}, Colors: colors,
	}
	paths := fixturePaths{
		mp4: filepath.Join(dir, "clip.mp4"),
		mxf: filepath.Join(dir, "clip.mxf"),
		rtp: filepath.Join(dir, "capture.rtp"),
	}
	if err := testdata.WriteMP4(paths.mp4, mp4Spec); err != nil {
		errs.Add(fault.New(fault.KindInvalidInput, err.Error(), paths.mp4, -1, false))
		return paths
	}
	// The unknown-KLV item is itself an anomaly class, so it is only injected
	// when a container corruption scenario is active. A clean run produces no
	// unknown-structure faults.
	injectUnknown := opts.Corruption == testdata.CorruptTruncatedKLV ||
		opts.Corruption == testdata.CorruptConflictMeta
	if err := testdata.WriteMXF(paths.mxf, testdata.MXFSpec{
		Seed: opts.Seed, Timescale: 30000, StartNanos: 500_000_000,
		Rotation: 180, FrameCount: 30, FrameSize: 128, KeyEvery: 10,
		UnknownKLV: injectUnknown, Colors: colors,
	}); err != nil {
		errs.Add(fault.New(fault.KindInvalidInput, err.Error(), paths.mxf, -1, false))
	}

	// Disorder and late arrival are exercised only when the RTP anomaly
	// toggles are active, keeping the clean capture fault-free.
	reorder, lateIdx := -1, -1
	if opts.WrapRTP || opts.Corruption == testdata.CorruptRTPWrap ||
		opts.Corruption == testdata.CorruptDrift {
		reorder, lateIdx = 5, 100
	}
	rtpSpec := testdata.RTPWrapSpec{
		ClockRate: opts.ClockRate, PacketSteps: 960, StartTS: 1000,
		DriftPPM: opts.DriftPPM, Packets: 120, Wrap: opts.WrapRTP,
		ReorderIndex: reorder, LateIndex: lateIdx,
	}
	pkts, late := testdata.BuildRTPPackets(rtpSpec)
	if err := testdata.WriteRTP(paths.rtp, pkts, late); err != nil {
		errs.Add(fault.New(fault.KindInvalidInput, err.Error(), paths.rtp, -1, false))
	}

	return applyCorruption(paths, opts, errs)
}

// applyCorruption mutates the relevant generated file and returns the updated
// fixture paths so the parser reads the damaged copy rather than the original.
func applyCorruption(paths fixturePaths, opts Options,
	errs *fault.Batch) fixturePaths {
	switch opts.Corruption {
	case testdata.CorruptTruncatedBox:
		dst := filepath.Join(filepath.Dir(paths.mp4), "trunc.mp4")
		if _, err := testdata.TruncateBox(paths.mp4, dst); err != nil {
			errs.Add(fault.New(fault.KindInvalidInput, err.Error(), dst, -1, false))
			return paths
		}
		paths.mp4 = dst
	case testdata.CorruptTruncatedKLV:
		dst := filepath.Join(filepath.Dir(paths.mxf), "trunc.mxf")
		if _, err := testdata.TruncateKLV(paths.mxf, dst); err != nil {
			errs.Add(fault.New(fault.KindInvalidInput, err.Error(), dst, -1, false))
			return paths
		}
		paths.mxf = dst
	}
	return paths
}
