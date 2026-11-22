package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediamerge/internal/clock"
	"mediamerge/internal/container"
	"mediamerge/internal/fixture"
	"mediamerge/internal/manifest"
	"mediamerge/internal/transport"
	"mediamerge/internal/txn"
)

// mergeCtx carries the state the merge and manifest stages need.
type mergeCtx struct {
	cfg         config
	workDir     string
	coord       *txn.Coordinator
	prov        *txn.Provenance
	rep         *report
	fs          *fixture.Set
	digests     map[string]string
	fi          *container.FileInfo
	tables      map[uint32]*container.SampleTable
	rtpFile     string
	tstats      transport.Stats
	skew        clock.Rational
	stageInputs func(...string) []txn.InputDigest
	crashed     func(string) (bool, error)
}

// finishPipeline runs the merge and manifest stages.
func finishPipeline(c *mergeCtx) (*report, error) {
	cfg, coord, prov, rep := c.cfg, c.coord, c.prov, c.rep

	// ---- stage: merge ----
	segs, actualBps, err := mergeSegments(cfg, c.fi, c.tables)
	if err != nil {
		return nil, err
	}
	segPath := filepath.Join(c.workDir, "out", "segments.bin")
	tmpSeg := filepath.Join(c.workDir, "tmp", "segments.bin.tmp")
	segDigest := digestOf(segs)
	mergeInputs := c.stageInputs(c.fs.NormalMP4, c.rtpFile)
	if done, err := stageDone(coord, prov, "merge", mergeInputs); err != nil {
		return nil, err
	} else if done {
		if err := checkRecovered(coord, "merge", segDigest); err != nil {
			return nil, err
		}
	} else {
		coord.RecordInputs("merge", mergeInputs)
		coord.Begin("merge")
		raw, _ := json.Marshal(segs)
		if err := os.WriteFile(tmpSeg, raw, 0o644); err != nil {
			return nil, err
		}
		coord.RecordTemp("merge", segDigest)
		err = coord.Commit("merge", mergeInputs, segDigest, []string{tmpSeg},
			func() error {
				got, err := sha256File(tmpSeg)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(raw)
				if got != hex.EncodeToString(sum[:]) {
					return fmt.Errorf("segment artifact hash mismatch")
				}
				return nil
			})
		if err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		if err := os.Rename(tmpSeg, segPath); err != nil {
			return nil, err
		}
	}
	rep.stages = append(rep.stages, "merge")
	if _, err := c.crashed("merge"); err != nil {
		return rep, err
	}

	// ---- stage: manifest ----
	m := &manifest.Manifest{
		Version:   1,
		Timescale: manifestTimescale,
		TargetBps: cfg.targetBps,
		ActualBps: actualBps,
		AVSkewNum: c.skew.Num, AVSkewDen: c.skew.Den,
		Quality: manifest.Quality{
			PacketLoss: c.tstats.Lost, RecoveredPacket: c.tstats.Recovered,
			Duplicates: c.tstats.Duplicates, GapCount: len(c.tstats.Gaps),
		},
		Hashes: map[string]string{},
	}
	for _, s := range segs {
		m.Segments = append(m.Segments, s)
		m.CommittedRanges = append(m.CommittedRanges, manifest.Range{Start: s.Start, End: s.End})
	}
	m.Hashes["segments.bin"] = segDigest
	m.Hashes["normal.mp4"] = c.digests[c.fs.NormalMP4]
	prov.AddDecision("manifest", "seal", fmt.Sprintf("%d segments, actual %d bps", len(segs), actualBps))
	manPath := filepath.Join(c.workDir, "out", "manifest.json")
	manInputs := mergeInputs
	injectFault := cfg.faultSet == "all" || cfg.faultSet == "manifest"
	done, err := stageDone(coord, prov, "manifest", manInputs)
	if err != nil {
		return nil, err
	}
	if !done {
		coord.RecordInputs("manifest", manInputs)
		coord.Begin("manifest")
		if injectFault {
			// Provisional commit, then fault 5: post-commit corruption.
			if err := m.Write(manPath); err != nil {
				return nil, err
			}
			if err := coord.Commit("manifest", manInputs, m.ContentSHA256, nil,
				func() error { return m.Validate() }); err != nil {
				return nil, fmt.Errorf("commit: %w", err)
			}
			if err := corruptFile(manPath); err != nil {
				return nil, err
			}
			if _, err := manifest.Read(manPath); err == nil {
				return nil, fmt.Errorf("invariant: corrupted manifest accepted")
			}
			rep.faults = append(rep.faults, faultCount{Stage: "manifest", Kind: "manifest-corruption", Count: 1})
			prov.AddDecision("manifest", "rollback", "post-commit corruption detected; rebuilding")
			if rbErr := coord.Commit("manifest", manInputs, "", []string{manPath},
				func() error { return fmt.Errorf("manifest content hash mismatch") }); rbErr == nil {
				return nil, fmt.Errorf("invariant: rollback commit unexpectedly succeeded")
			}
		}
	} else if injectFault && coord.StageRollbacks["manifest"] > 0 {
		// Re-derive the corruption fault and rollback decision from the
		// recovered WAL so a resumed run reports identically.
		rep.faults = append(rep.faults, faultCount{Stage: "manifest", Kind: "manifest-corruption", Count: 1})
		prov.AddDecision("manifest", "rollback", "post-commit corruption detected; rebuilding")
	}
	// Final seal: provenance is complete only after every decision above.
	m.ProvenanceSHA256 = prov.Digest()
	if !done {
		if err := m.Write(manPath); err != nil {
			return nil, err
		}
		if err := coord.Commit("manifest", manInputs, m.ContentSHA256, nil,
			func() error { return m.Validate() }); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
	} else {
		m.Seal()
		if err := checkRecovered(coord, "manifest", m.ContentSHA256); err != nil {
			return nil, err
		}
	}
	if _, err := manifest.Read(manPath); err != nil {
		return nil, fmt.Errorf("invariant: %w", err)
	}
	rep.stages = append(rep.stages, "manifest")
	rep.m = m
	rep.rollbacks = coord.Rollbacks
	rep.provSHA = prov.Digest()
	return rep, nil
}
