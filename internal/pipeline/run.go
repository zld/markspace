package pipeline

import (
	"github.com/pion/rtp"

	"gamepack/internal/align"
	"gamepack/internal/fixture"
	"gamepack/internal/manifest"
	perr "gamepack/internal/errors"
	"gamepack/internal/recovery"
	"gamepack/internal/source"
	"gamepack/internal/stream"
	"gamepack/internal/txn"
)

// Run executes (or resumes) the full job to an atomic manifest commit. Stages
// already marked verified in the WAL are skipped, including their log
// transitions; execution resumes at plan.ResumeStage.
func (r *Runner) Run() (*Report, error) {
	plan, err := recovery.Inspect(r.dirs.txnDir)
	if err != nil {
		return nil, err
	}
	r.plan = plan
	startIdx := stageIdx(plan.ResumeStage)

	// STAGE: scan — generate fixtures, scan each rendition and attach RTP.
	var reps []*align.Representation
	var inputHash string
	if startIdx <= stageIdx(txn.StageScan) {
		if err := r.record(txn.StageScan, txn.StateStarted, "0", "", nil); err != nil {
			return nil, err
		}
		if err := r.maybeCrash(txn.StageScan); err != nil {
			return nil, err
		}
		reps, _, inputHash, err = r.stageScan()
		if err != nil {
			_ = r.record(txn.StageScan, txn.StateFailed, "0", inputHash, nil)
			return nil, err
		}
		if err := r.record(txn.StageScan, txn.StateVerified, "scan-ok", inputHash, nil); err != nil {
			return nil, err
		}
	} else {
		// Re-derive in-memory state for already-verified stages; no WAL append.
		reps, _, inputHash, err = r.stageScan()
		if err != nil {
			return nil, err
		}
	}

	// STAGE: align — normalize sources and build the common-cut ladder.
	var ladder *align.Ladder
	if startIdx <= stageIdx(txn.StageAlign) {
		if err := r.record(txn.StageAlign, txn.StateStarted, "scan-ok", inputHash, nil); err != nil {
			return nil, err
		}
		if err := r.maybeCrash(txn.StageAlign); err != nil {
			return nil, err
		}
		ladder, err = r.stageAlign(reps)
		if err != nil {
			_ = r.record(txn.StageAlign, txn.StateFailed, "scan-ok", inputHash, nil)
			return nil, err
		}
		if err := r.record(txn.StageAlign, txn.StateVerified, "align-ok", inputHash, nil); err != nil {
			return nil, err
		}
	} else {
		ladder, err = r.stageAlign(reps)
		if err != nil {
			return nil, err
		}
	}

	// STAGE: mux — write one MP4 fragment per representation per segment.
	var entries []manifest.Entry
	var zeroDiffs int
	if startIdx <= stageIdx(txn.StageMux) {
		if err := r.record(txn.StageMux, txn.StateStarted, "align-ok", inputHash, nil); err != nil {
			return nil, err
		}
		if err := r.maybeCrash(txn.StageMux); err != nil {
			return nil, err
		}
		entries, zeroDiffs, err = r.stageMux(reps, ladder)
		if err != nil {
			_ = r.record(txn.StageMux, txn.StateFailed, "align-ok", inputHash, nil)
			_ = recovery.MarkRollback(r.wal, txn.StageMux, "align-ok")
			_ = recovery.RemoveStageOutputs(r.dirs.segs)
			return nil, err
		}
		if err := r.record(txn.StageMux, txn.StateVerified, "mux-ok", inputHash, nil); err != nil {
			return nil, err
		}
	} else {
		entries, zeroDiffs, err = r.stageMux(reps, ladder)
		if err != nil {
			return nil, err
		}
	}

	// STAGE: verify — validate every fragment structurally.
	if startIdx <= stageIdx(txn.StageVerify) {
		if err := r.record(txn.StageVerify, txn.StateStarted, "mux-ok", inputHash, nil); err != nil {
			return nil, err
		}
		if err := r.maybeCrash(txn.StageVerify); err != nil {
			return nil, err
		}
		if r.cfg.FailVerify && !r.verifyFailConsumed {
			r.verifyFailConsumed = true
			forced := perr.New("pipeline", perr.CodeChecksum,
				"injected verification failure for rollback test")
			_ = r.record(txn.StageVerify, txn.StateFailed, "mux-ok", inputHash, nil)
			_ = recovery.MarkRollback(r.wal, txn.StageVerify, "mux-ok")
			_ = recovery.RemoveStageOutputs(r.dirs.segs)
			return nil, forced
		}
		if err := r.stageVerify(entries); err != nil {
			_ = r.record(txn.StageVerify, txn.StateFailed, "mux-ok", inputHash, nil)
			_ = recovery.MarkRollback(r.wal, txn.StageVerify, "mux-ok")
			_ = recovery.RemoveStageOutputs(r.dirs.segs)
			return nil, err
		}
		if err := r.record(txn.StageVerify, txn.StateVerified, "verify-ok", inputHash, nil); err != nil {
			return nil, err
		}
	} else {
		if err := r.stageVerify(entries); err != nil {
			return nil, err
		}
	}

	// STAGE: commit — finalize and atomically publish the manifest.
	if startIdx <= stageIdx(txn.StageCommit) {
		if err := r.record(txn.StageCommit, txn.StateStarted, "verify-ok", inputHash, nil); err != nil {
			return nil, err
		}
		if err := r.maybeCrash(txn.StageCommit); err != nil {
			return nil, err
		}
	}
	rep, err := r.stageCommit(entries, inputHash, plan)
	if err != nil {
		_ = r.record(txn.StageCommit, txn.StateFailed, "verify-ok", inputHash, nil)
		return nil, err
	}
	if err := r.record(txn.StageCommit, txn.StateVerified, "done", inputHash, nil); err != nil {
		return nil, err
	}
	rep.Rollbacks = r.wal.RollbackCount()
	rep.StageLog = r.wal.Records()
	rep.ResumeStage = plan.ResumeStage
	rep.ZeroTickDiffs = zeroDiffs
	return rep, nil
}

// stageIdx maps a stage to its ordered index, treating unknown/empty as scan.
func stageIdx(s txn.Stage) int {
	for i, st := range txn.OrderedStages {
		if st == s {
			return i
		}
	}
	return 0
}

func (r *Runner) stageScan() ([]*align.Representation, []*fixture.Artifact, string, error) {
	var arts []*fixture.Artifact
	var reps []*align.Representation
	for _, codec := range r.cfg.Codecs {
		for _, br := range r.cfg.Bitrates {
			art, err := fixture.Generate(r.dirs.inputs, fixture.Options{
				Codec: codec, BitrateKbps: br, Units: r.cfg.Units,
				KeyEvery: r.cfg.KeyEvery, WrapBits: r.cfg.WrapBits,
				Injections: r.cfg.Inject,
			})
			if err != nil {
				return nil, nil, "", perr.New("pipeline", perr.CodeStage, "fixture: %v", err)
			}
			scan, err := stream.ScanFile(codec, art.StreamPath, art.IndexPath)
			if err != nil {
				return nil, nil, "", err
			}
			pkts, err := stream.ReadRTPSource(art.RTPPath)
			if err != nil {
				return nil, nil, "", err
			}
			if err := stream.AttachTransportCoordinates(scan, pkts); err != nil {
				return nil, nil, "", err
			}
			if err := stream.ValidateBoundaries(scan); err != nil {
				return nil, nil, "", err
			}
			samples, err := normalize(pkts, scan, r.cfg.WrapBits, r.cfg.Units/2)
			if err != nil {
				return nil, nil, "", err
			}
			id := fmtID(codec, br)
			reps = append(reps, &align.Representation{
				ID: id, Codec: codec, BitrateKbps: br, Scan: scan, Samples: samples,
			})
			arts = append(arts, art)
		}
	}
	inputHash, err := r.inputDigest(arts)
	if err != nil {
		return nil, nil, "", err
	}
	return reps, arts, inputHash, nil
}

func normalize(pkts []*rtp.Packet, scan *stream.ScanResult,
	wrapBits uint, switchAt int) ([]source.Sample, error) {
	n, err := source.NewNormalizer(source.Config{BitWidth: wrapBits})
	if err != nil {
		return nil, err
	}
	for i, p := range pkts {
		if i == switchAt {
			n.SwitchAt()
		}
		if err := n.Push(i, p.Timestamp, scan.Units[i].Start); err != nil {
			return nil, err
		}
	}
	return n.Samples(), nil
}
