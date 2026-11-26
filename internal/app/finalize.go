package app

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"

	"rtpregress/internal/audio"
	"rtpregress/internal/clock"
	"rtpregress/internal/manifest"
	"rtpregress/internal/pipeline"
	"rtpregress/internal/rtp"
	"rtpregress/internal/ts"
)

// referenceToneHz is the deterministic fixture tone used for SNR analysis.
const referenceToneHz = 440.0

// replayCompleted re-adds checkpointed segments to the planner and merger so a
// resumed run reaches exactly the same ordered output as a fresh one.
func replayCompleted(cp *Checkpoint, planner *audio.Planner, merger *pipeline.Merger,
	placements map[int]audio.Placement) error {
	for _, row := range cp.Completed {
		pl, err := planner.Place(audio.Segment{
			ID: row.SegNumber, PID: row.PID, StartNanos: row.StartNanos,
		}, row.Samples)
		if err != nil {
			return err
		}
		placements[row.SegNumber] = pl
		// Reload the exact canonical PCM so a resumed assembly is byte-for-byte
		// identical to a fresh run, regardless of the worker count.
		pcm, _ := os.ReadFile(row.PCMFile)
		merger.Add(pipeline.PoolResult{
			Job: pipeline.Job{
				Segment: audio.Segment{ID: row.SegNumber, PID: row.PID,
					StartNanos: row.StartNanos},
				PID: row.PID, SegNumber: row.SegNumber,
			},
			Result: &audio.Result{
				SegmentID: row.SegNumber, StartNanos: row.StartNanos,
				Samples: row.Samples, PCM: pcm,
				PCMSHA256: decodeHash(row.PCMSHA256),
			},
		}, pl)
	}
	return nil
}

func decodeHash(s string) [32]byte {
	var out [32]byte
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return out
	}
	copy(out[:], raw)
	return out
}

// persistSegment writes one segment's canonical PCM and returns a content-
// addressed checkpoint row. The PCM is the deterministic resume/hash source.
func persistSegment(aacDir string, r pipeline.PoolResult,
	pl audio.Placement) CompletedSegment {
	pcmName := segmentBase(aacDir, r.Job.SegNumber) + ".pcm"
	_ = os.WriteFile(pcmName, r.Result.PCM, 0o644)
	st := audio.MeasurePCM(r.Result.PCM, audio.TargetRate, referenceToneHz)
	return CompletedSegment{
		SegNumber:   r.Job.SegNumber,
		PID:         r.Job.PID,
		StartNanos:  r.Result.StartNanos,
		StartSample: pl.StartSample,
		Samples:     r.Result.Samples,
		PCMSHA256:   hex.EncodeToString(r.Result.PCMSHA256[:]),
		PCMFile:     pcmName,
		MeanSquare:  st.MeanSquare,
		TruePeak:    st.TruePeak,
		Sinad:       st.Sinad,
	}
}

// finalize assembles one continuous PCM timeline, normalizes and encodes it
// once, validates the fMP4 timing and measures quality on the delivered audio.
func finalize(opt Options, cp *Checkpoint, planner *audio.Planner,
	merger *pipeline.Merger, gaps []ts.Gap, psi []*ts.PSIEvent,
	dstats ts.Stats, rstats rtp.Stats) (*RunResult, error) {
	ctx := context.Background()
	ordered := merger.Sorted()
	totalSamples := planner.TotalSamples()

	// 1. Continuous canonical PCM: each segment at its sample offset, gaps
	// bridged with silence (already reported explicitly in the manifest).
	pcmBySeg := make(map[int][]byte, len(ordered))
	placements := make([]audio.Placement, 0, len(ordered))
	for _, s := range ordered {
		pcmBySeg[s.SegNumber] = s.PCM
		placements = append(placements, audio.Placement{
			SegmentID: s.SegNumber, PID: s.PID, StartSample: s.StartSample,
			StartNanos: s.StartNanos, Samples: s.Samples,
		})
	}
	rawPCM := audio.AssemblePCM(placements, pcmBySeg, totalSamples)

	conv, err := audio.NewConverter(opt.Bitrate)
	if err != nil {
		return nil, err
	}
	// 2. Program-wide two-pass loudness normalization (single operation).
	normalized, err := conv.NormalizeProgram(ctx, rawPCM)
	if err != nil {
		return nil, err
	}
	// 3. The single AAC encoding pass produces the delivered fragmented MP4.
	fmp4Path := filepath.Join(opt.OutDir, "output.m4s")
	if err := conv.EncodeFMp4(ctx, normalized, fmp4Path); err != nil {
		return nil, err
	}
	timing, verr := manifest.ValidateFMp4(fmp4Path)
	if verr != nil {
		return nil, verr
	}
	maxErrMS := fragmentSpacingErrorMS(timing)

	// 4. Measure on the DELIVERED audio: decode the fMP4 back to PCM, then
	// compare against the normalized pre-encode PCM for codec-fidelity SNR
	// (both share the same gain, so only codec error counts as noise), and run
	// the authoritative EBU R128 analysis for LUFS and true peak.
	deliveredPCM, derr := conv.DecodeFMp4(ctx, fmp4Path)
	if derr != nil {
		return nil, derr
	}
	snr := audio.SNRDelivered(normalized, deliveredPCM)
	loud, lerr := conv.MeasureLoudness(ctx, deliveredPCM)
	if lerr != nil {
		return nil, lerr
	}
	// Bitrate uses the actual encoded-content duration (the PCM length fed to
	// the single encoder), not the timeline span which includes leading offset
	// and silence gaps.
	contentSamples := int64(len(normalized) / audio.PCMBytesPerSample())
	contentDurationNS := contentSamples * clock.NanosPerSecond / int64(audio.TargetRate)
	timelineDurationNS := planner.TimeBase().SampleToNanos(totalSamples)
	var actual int64
	if timing.ESSampleBytes > 0 && contentDurationNS > 0 {
		actual = audio.BitrateActual(int(timing.ESSampleBytes), contentDurationNS)
	}
	metrics := audio.Metrics{
		BitrateTarget:      int64(opt.Bitrate),
		BitrateActual:      actual,
		BitrateDeltaPct:    audio.BitrateDeltaPct(actual, int64(opt.Bitrate)),
		SNRDB:              snr,
		LUFS:               loud.IntegratedLUFS,
		TruePeakDBTP:       loud.TruePeakDBTP,
		ManifestMaxErrorMS: maxErrMS,
	}

	mb := manifest.NewBuilder(opt.Rate, opt.Channels, opt.Bitrate)
	for _, s := range ordered {
		mb.AddSegment(manifest.SegmentEntry{
			Segment: s.SegNumber, PID: manifest.HexPID(s.PID),
			StartNanos: s.StartNanos, StartSample: s.StartSample,
			Samples: s.Samples, PCMSHA256: s.PCMSHA256,
		})
	}
	for _, g := range gaps {
		mb.AddGap(manifest.GapEntry{
			PID: manifest.HexPID(g.PID), Offset: g.Offset,
			FromNanos: g.FromNanos, ToNanos: g.ToNanos,
			Code: string(g.Code), Detail: g.Detail,
		})
	}
	for _, p := range psi {
		mb.AddPSI(manifest.PSIEntry{
			Table: p.Kind, Offset: p.Offset, Version: p.Version,
			TableIDExt: p.TableIDExt,
		})
	}
	mb.SetTimeline(totalSamples, timelineDurationNS, dstats.DriftPPM)
	mb.SetRTP(manifest.RTPStats{
		Received: rstats.Received, InOrder: rstats.InOrder,
		Duplicates: rstats.Duplicates, Late: rstats.Late, Lost: rstats.Lost,
	})
	mb.SetHash(cp.RunHash)
	mb.SetMetrics(metrics)
	manifestPath, err := mb.Write(opt.OutDir, "manifest.json")
	if err != nil {
		return nil, err
	}
	return &RunResult{
		ExitCode: ExitOK, Checkpoint: cp, Stats: dstats,
		Manifest: manifestPath, FMp4: fmp4Path, Metrics: metrics,
		MaxErrorMS: maxErrMS,
	}, nil
}

// fragmentSpacingErrorMS measures the largest deviation between successive
// fragment decode-time deltas and their expected fixed-frame spacing. It is
// insensitive to a constant encoder-priming offset on the first fragment.
func fragmentSpacingErrorMS(tm *manifest.FMp4Timing) float64 {
	if len(tm.FragmentBase) < 3 {
		return 0
	}
	const frameSamples = 1024 // one AAC frame at 48 kHz
	maxErr := 0.0
	for i := 1; i < len(tm.FragmentBase); i++ {
		got := int64(tm.FragmentBase[i]) - int64(tm.FragmentBase[i-1])
		delta := got - frameSamples
		if delta < 0 {
			delta = -delta
		}
		ms := float64(delta) * 1000.0 / float64(audio.TargetRate)
		if ms > maxErr {
			maxErr = ms
		}
	}
	return maxErr
}
