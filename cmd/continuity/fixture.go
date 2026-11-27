package main

import (
	"os"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/mux"
	"github.com/zld/continuity/internal/report"
)

// targetBPS is the notional encoding budget the synthetic fixture is sized to.
// Samples are a fixed 64 bytes at 25 fps, so actual bitrate is deterministic;
// target is set to that same value to keep deviation at zero for the success
// case while still exercising the deviation arithmetic.
const targetBPS int64 = 64 * 8 * 25 // 12800 bits/s

// writeDemoFixture generates a single-video-track MP4 entirely in Go. No
// external media is referenced; the same routine can emit structural faults.
func writeDemoFixture(path string, d demoFlags, fault mux.Faults) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	specs := make([]mux.SampleSpec, d.samples)
	for i := range specs {
		specs[i] = mux.SampleSpec{
			Delta: 1, // one tick per frame on the 25 Hz timescale
			Sync:  d.syncEvery > 0 && i%d.syncEvery == 0,
		}
	}
	tracks := []mux.TrackSpec{{Timescale: 25, Video: true, Samples: specs}}
	if _, err := mux.WriteFile(f, tracks, fault); err != nil && fault == mux.FaultNone {
		return err
	}
	return nil
}

type bitratePair struct {
	target int64
	actual int64
}

// demoBitrate derives actual bitrate from the real on-disk sample sizes and an
// exact duration computed from the track timescale, then pairs it with the
// notional target. The duration stays rational until the final float render.
func demoBitrate(in *demux.File) bitratePair {
	var totalBytes int64
	maxDur := clock.Zero()
	for _, tr := range in.Tracks {
		for _, s := range tr.Samples {
			totalBytes += int64(s.Size)
			end, err := s.DTS.Add(s.Duration)
			if err == nil && end.Cmp(maxDur) > 0 {
				maxDur = end
			}
		}
	}
	return bitratePair{
		target: targetBPS,
		actual: report.Bitrate(totalBytes, maxDur.Seconds()),
	}
}

// demoPSNR re-reads every sample independently through the demux handle and
// compares it against the deterministic bytes the generator would produce. A
// matching byte plane means every indexed offset resolves to valid media; the
// undamaged success case therefore reports an effectively infinite score, which
// is rendered as a finite sentinel so the JSON field stays a plain number.
func demoPSNR(in *demux.File) float64 {
	var reference, candidate []byte
	for ti, tr := range in.Tracks {
		for i := range tr.Samples {
			raw, err := in.ReadSample(uint32(ti), uint32(i))
			if err != nil {
				return 0
			}
			candidate = append(candidate, raw...)
			reference = append(reference, expectedSampleBytes(ti, i, len(raw))...)
		}
	}
	db, err := report.PSNR(reference, candidate, 255)
	if err != nil {
		return 0
	}
	if db > 99.0 { // +Inf for identical planes; report a bounded sentinel
		return 99.0
	}
	return db
}

// expectedSampleBytes mirrors mux.sampleBytes without exporting the generator.
func expectedSampleBytes(track, sample, size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte((track*31 + sample*17 + i*7) & 0xff)
	}
	return out
}
