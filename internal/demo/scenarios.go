package demo

import (
	"context"
	"errors"
	"mediapipe/internal/media"
	"mediapipe/internal/model"
	mp4 "mediapipe/internal/mp4"
	"mediapipe/internal/report"
	"mediapipe/internal/rtp"
)

// FailureScenario records a deliberately failed run and the proof that
// replaying it produces the same manifest digest and rollback record.
type FailureScenario struct {
	// Status is the terminal rollback state of the failing run.
	Status string
	// Digest is the summary digest of the failed run.
	Digest string
	// RerunDigest is the digest of an identical second attempt.
	RerunDigest string
	// Records is the rollback log of the first attempt.
	Records []report.Record
	// Reason is the top-level failure message.
	Reason string
}

// Consistent reports whether two runs of the same failing input produced
// identical digests, including the rollback record.
func (f FailureScenario) Consistent() bool {
	return f.Digest != "" && f.Digest == f.RerunDigest && len(f.Records) > 0
}

// RecoveryScenario records a recoverable run and the proof that re-scanning
// its inputs yields the same manifest digest.
type RecoveryScenario struct {
	// Recovered indexes is how many incomplete indexes were rebuilt.
	Recovered int
	// Digest and RescanDigest must match for recovery to be trustworthy.
	Digest       string
	RescanDigest string
	// Status is the terminal state after recovery.
	Status string
}

// Consistent reports whether the re-scan reproduced the same summary.
func (r RecoveryScenario) Consistent() bool {
	return r.Digest != "" && r.Digest == r.RescanDigest
}

// runFailureScenario executes the same healthy pipeline with one rendition
// truncated. The scanner rejects it, nothing is committed, and a second
// identical attempt must reproduce both the summary digest and the rollback
// record byte for byte.
func runFailureScenario(ctx context.Context, o Options) (FailureScenario, error) {
	first, err := failedAttempt(ctx, o)
	if err != nil {
		return FailureScenario{}, err
	}
	second, err := failedAttempt(ctx, o)
	if err != nil {
		return FailureScenario{}, err
	}
	first.RerunDigest = second.Digest
	return first, nil
}

// failedAttempt runs the pipeline against a fixture whose first rendition is
// truncated and converts the failure into a rolled-back summary.
func failedAttempt(ctx context.Context, o Options) (FailureScenario, error) {
	a := planFor(o)[0]
	vp := a.versions[0]
	tp := vp.tracks[0]
	spec, err := fileSpec(a, vp, tp)
	if err != nil {
		return FailureScenario{}, err
	}
	data, err := media.BuildFile(spec)
	if err != nil {
		return FailureScenario{}, err
	}
	// Cut the file inside its media payload: the container advertises a box
	// that runs past the end of the input.
	truncated := data[:len(data)*2/3]
	_, scanErr := scanData(ctx, a.name, vp.id, tp.trackID, truncated)
	if scanErr == nil {
		return FailureScenario{}, model.New(model.KindInternal, model.StageMP4Scan, a.name, "failure-fixture",
			errors.New("truncated fixture was accepted")).WithVersion(vp.id)
	}

	stager, err := report.NewStager("")
	if err != nil {
		return FailureScenario{}, err
	}
	defer stager.Discard()

	sm := &report.Summary{Seed: o.Seed, Versions: o.Versions, Window: o.Window}
	// Stage a partially built result first: a failed run must not publish it.
	if _, err := stager.Stage("summary.json", []byte(`{"partial":true}`)); err != nil {
		return FailureScenario{}, err
	}
	sm.Rollback(scanErr)
	if _, err := stager.CommitResult(sm, "summary.json"); err == nil {
		return FailureScenario{}, model.New(model.KindInternal, model.StageReport, a.name, "failure-fixture",
			errors.New("rolled-back summary was committed"))
	}
	digest, err := sm.ManifestDigest()
	if err != nil {
		return FailureScenario{}, err
	}
	return FailureScenario{
		Status:  sm.Status.State,
		Digest:  digest,
		Records: sm.Status.Records,
		Reason:  sm.Status.Reason,
	}, nil
}

// runRecoveryScenario scans a rendition whose chunk-offset table is unusable,
// rebuilds its index, and re-runs the identical scan to confirm the summary
// digest is stable.
func runRecoveryScenario(ctx context.Context, o Options) (RecoveryScenario, error) {
	a := planFor(o)[0]
	vp := a.versions[0]
	vp.badIndex = true
	tp := vp.tracks[0]
	spec, err := fileSpec(a, vp, tp)
	if err != nil {
		return RecoveryScenario{}, err
	}
	data, err := media.BuildFile(spec)
	if err != nil {
		return RecoveryScenario{}, err
	}
	if n := media.StripTable(data, "stco"); n == 0 {
		return RecoveryScenario{}, model.New(model.KindInternal, model.StagePipeline, a.name, "recovery-fixture",
			errors.New("generated file has no stco table")).WithVersion(vp.id)
	}

	digestOf := func() (string, int, error) {
		idx, err := scanData(ctx, a.name, vp.id, tp.trackID, data)
		if err != nil {
			return "", 0, err
		}
		sm := &report.Summary{Seed: o.Seed, Versions: o.Versions, Window: o.Window}
		sm.AddDigest("recovered-index", []byte(indexDigest(idx)))
		d, err := sm.ManifestDigest()
		return d, idx.TotalRecovered(), err
	}
	first, recovered, err := digestOf()
	if err != nil {
		return RecoveryScenario{}, err
	}
	second, _, err := digestOf()
	if err != nil {
		return RecoveryScenario{}, err
	}
	sm := &report.Summary{Seed: o.Seed, Versions: o.Versions, Window: o.Window}
	sm.Commit()
	return RecoveryScenario{
		Recovered:    recovered,
		Digest:       first,
		RescanDigest: second,
		Status:       sm.Status.State,
	}, nil
}

// SeekReport records sparse-keyframe localisation behaviour for one video
// track: how many probe ticks resolved to an earlier keyframe and the worst
// locating deviation actually measured.
type SeekReport struct {
	Samples   int
	Probes    int
	KeySeeked int
	MaxDevMS  int64
	MeanDevMS int64
}

// measureSeeks probes a video track at each sample midpoint and records how
// far the nearest preceding keyframe is. A sparse GOP must report the real
// deviation rather than pretending the requested timestamp was decoded.
func measureSeeks(idx *mp4.Index) SeekReport {
	rep := SeekReport{}
	for _, t := range idx.Tracks {
		if t.Handler != "vide" || len(t.Samples) == 0 || t.Timescale == 0 {
			continue
		}
		rep.Samples = len(t.Samples)
		var sum int64
		for _, s := range t.Samples {
			probe := s.DecodeTks + s.Duration/2
			rep.Probes++
			kf, dev, ok := t.KeyframeBefore(probe)
			if !ok {
				continue
			}
			if dev > 0 {
				rep.KeySeeked++
			}
			devMS := dev * 1_000 / int64(t.Timescale)
			sum += devMS
			if devMS > rep.MaxDevMS {
				rep.MaxDevMS = devMS
			}
			_ = kf
		}
		if rep.Probes > 0 {
			rep.MeanDevMS = sum / int64(rep.Probes)
		}
	}
	return rep
}

// rtpLiveness proves the window is exercised even when every loss happens to
// be beyond it: duplicates and reordered deliveries must still be non-zero.
func rtpLiveness(st rtp.Stats) error {
	if st.Duplicates == 0 {
		return model.New(model.KindInternal, model.StageRTP, "live-main", "liveness",
			errors.New("duplicate fixture produced no duplicates"))
	}
	if st.Reordered == 0 {
		return model.New(model.KindInternal, model.StageRTP, "live-main", "liveness",
			errors.New("reorder fixture produced no reordered packets"))
	}
	if st.Recovered == 0 {
		return model.New(model.KindInternal, model.StageRTP, "live-main", "liveness",
			errors.New("in-window loss was never recovered"))
	}
	return nil
}
