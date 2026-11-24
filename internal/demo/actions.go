package demo

import (
	"os"
	"path/filepath"

	"medpipe/internal/demux"
	"medpipe/internal/fault"
	"medpipe/internal/mux"
	"medpipe/internal/pipeline"
	"medpipe/internal/samplegen"
	"medpipe/internal/txn"
)

func commitHealthy(dir, finalName string, ts []byte, rules []demux.MapRule) (*SampleReport, *demux.Media, *txn.Marker, *fault.Error) {
	sr := &SampleReport{Kind: "normal", InputPath: filepath.Join(dir, "input.ts"), OutputPath: filepath.Join(dir, finalName), ExitCode: fault.ExitOK}
	inputSHA := pipeline.InputDigest(ts)
	mappingSHA := pipeline.MappingDigest(rules)
	mgr, fe := txn.NewManager(dir)
	if fe != nil {
		sr.ExitCode = fault.ExitInternal
		return sr, nil, nil, fe
	}
	defer mgr.Close()

	tx, fe := mgr.Begin(finalName, inputSHA, mappingSHA)
	if fe != nil {
		sr.ExitCode = fault.ExitInternal
		return sr, nil, nil, fe
	}
	if fe := tx.Stage("demux_map", "parse TS and apply explicit mapping"); fe != nil {
		return sr, nil, nil, fe
	}
	res, media, events, derr := pipeline.ProcessTSBytes(ts, tx.TempPath, rules)
	sr.EventCount = len(events)
	sr.GapEvents = countGapEvents(events)
	if derr != nil {
		_ = tx.Rollback(derr.Message)
		sr.ExitCode = fault.ExitCode(derr)
		applyFaultError(sr, derr)
		return sr, media, nil, derr
	}
	if fe := tx.Prepare(res.SHA256, res.Bytes); fe != nil {
		_ = tx.Rollback("prepare failed")
		return sr, media, nil, fe
	}
	marker, fe := tx.Commit()
	if fe != nil {
		_ = tx.Rollback("commit failed")
		return sr, media, nil, fe
	}
	sr.OutputSHA256 = marker.OutputSHA256
	if media != nil {
		sr.PSIVersion = media.PSIVersion
	}
	return sr, media, marker, nil
}

func runResume(dir string, ts []byte, rules []demux.MapRule, resume bool) (*SampleReport, string, *fault.Error) {
	finalName := "resume.mp4"
	sr := &SampleReport{Kind: "resume", OutputPath: filepath.Join(dir, finalName), ExitCode: fault.ExitOK}
	inputSHA := pipeline.InputDigest(ts)
	mappingSHA := pipeline.MappingDigest(rules)
	mgr, fe := txn.NewManager(dir)
	if fe != nil {
		return sr, "", fe
	}
	tx, fe := mgr.Begin(finalName, inputSHA, mappingSHA)
	if fe != nil {
		mgr.Close()
		return sr, "", fe
	}
	res, _, _, derr := pipeline.ProcessTSBytes(ts, tx.TempPath, rules)
	if derr != nil {
		_ = tx.Rollback(derr.Message)
		mgr.Close()
		return sr, "", derr
	}
	if fe := tx.Prepare(res.SHA256, res.Bytes); fe != nil {
		_ = tx.Rollback("prepare failed")
		mgr.Close()
		return sr, "", fe
	}
	sr.PreResumeSHA256 = res.SHA256
	// Simulate process termination after the prepared temp file and WAL are
	// durable, but before atomic rename/commit marker. Closing the handle is
	// the in-process equivalent; no committed result exists yet.
	mgr.Close()

	if !resume {
		mgr2, _ := txn.NewManager(dir)
		rec, _ := mgr2.Recover(mux.VerifyReadable)
		sr.Recovery = rec
		mgr2.Close()
		return sr, "", fault.New(fault.ErrResumeUnsupported, "demo.resume", 0, "--resume is required")
	}

	mgr2, fe2 := txn.NewManager(dir)
	if fe2 != nil {
		return sr, "", fe2
	}
	rec, rerr := mgr2.Recover(mux.VerifyReadable)
	mgr2.Close()
	sr.Recovery = rec
	if rerr != nil {
		return sr, "", rerr
	}
	sr.OutputSHA256 = rec.OutputSHA256
	if rec.OutputSHA256 != sr.PreResumeSHA256 {
		return sr, rec.OutputSHA256, fault.New(fault.ErrDigestMismatch, "demo.resume", 0, "temp digest changed across recovery")
	}
	return sr, rec.OutputSHA256, nil
}

func runCorrupt(dir string, seed int64, faults map[string]bool, rules []demux.MapRule) (*SampleReport, []fault.Event, int, *fault.Error) {
	sr := &SampleReport{Kind: "corrupt", InputPath: filepath.Join(dir, "input.ts"), OutputPath: filepath.Join(dir, "corrupt.mp4"), ExitCode: fault.ExitCorrupt}
	base := samplegen.BuildVersionSwitchTS(seed, faults["psi-version"])
	if faults["cc-jump"] {
		if _, err := samplegen.ApplyCCJump(base, samplegen.VideoPID, 1); err != nil {
			return sr, nil, 0, fault.Wrap(fault.ErrInternal, "demo.fixture.cc", 0, "apply CC jump", err)
		}
	}
	networkTS, rtpStats, netStats, _ := runRTP(base, seed+101, faults)
	sr.RTPStats = &rtpStats
	sr.NetworkStats = &netStats
	corruptTS := networkTS
	if faults["truncate"] {
		corruptTS = samplegen.Truncate(networkTS, 37)
	}
	if err := os.WriteFile(sr.InputPath, corruptTS, 0o644); err != nil {
		return sr, nil, 0, fault.Wrap(fault.ErrInternal, "demo.corrupt.write", 0, "write corrupt fixture", err)
	}

	inputSHA := pipeline.InputDigest(corruptTS)
	mappingSHA := pipeline.MappingDigest(rules)
	mgr, fe := txn.NewManager(dir)
	if fe != nil {
		return sr, nil, 0, fe
	}
	defer mgr.Close()
	tx, fe := mgr.Begin("corrupt.mp4", inputSHA, mappingSHA)
	if fe != nil {
		return sr, nil, 0, fe
	}
	_, media, events, perr := pipeline.ProcessTSBytes(corruptTS, tx.TempPath, rules)
	if media != nil {
		sr.PSIVersion = media.PSIVersion
	}
	sr.EventCount = len(events)
	sr.GapEvents = countGapEvents(events)
	sr.ErrorCodes = uniqueCodes(events)
	if perr == nil {
		_ = tx.Rollback("unexpected acceptance")
		return sr, events, mgr.RollbackCount, nil
	}
	applyFaultError(sr, perr)
	_ = tx.Rollback(perr.Message)
	if _, err := os.Stat(tx.FinalPath); !os.IsNotExist(err) {
		return sr, events, mgr.RollbackCount, fault.New(fault.ErrCommit, "demo.corrupt", 0, "corrupt output was committed")
	}
	if _, err := os.Stat(tx.MarkerPath); !os.IsNotExist(err) {
		return sr, events, mgr.RollbackCount, fault.New(fault.ErrCommit, "demo.corrupt", 0, "corrupt commit marker exists")
	}
	return sr, events, mgr.RollbackCount, perr
}

func uniqueCodes(events []fault.Event) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		code := string(e.Code)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func applyFaultError(sr *SampleReport, fe *fault.Error) {
	if sr == nil || fe == nil {
		return
	}
	sr.ErrorCode = fe.Code
	sr.ErrorStage = fe.Stage
	sr.ErrorOffset = fe.Offset
	sr.ErrorMessage = fe.Message
	if fe.HasPID {
		sr.ErrorPID = fe.PID
	}
}
