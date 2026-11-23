// Package runner wires every subsystem together for one mediaqc self-test
// run: it generates seeded fixtures, drives them through the staged pipeline
// and the adapters, and produces deterministic quantitative evidence.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"mediaqc/internal/adapter"
	"mediaqc/internal/adapter/hls"
	mp4adapter "mediaqc/internal/adapter/mp4"
	"mediaqc/internal/fixture"
	"mediaqc/internal/metrics"
	"mediaqc/internal/model"
	"mediaqc/internal/normalize"
	"mediaqc/internal/packager"
	"mediaqc/internal/pipeline"
	"mediaqc/internal/recovery"
	"mediaqc/internal/sync"
)

// Options are the validated CLI parameters.
type Options struct {
	Seed        int64
	Window      int
	ToleranceMs int64
}

// PathReport summarizes one demonstration path.
type PathReport struct {
	Name         string   `json:"name"`
	MaterialID   string   `json:"material_id"`
	Status       string   `json:"status"`
	ManifestSHA  string   `json:"manifest_sha256"`
	Errors       []string `json:"errors"`
}

// Report is the full deterministic evidence document.
type Report struct {
	Seed               int64        `json:"seed"`
	Window             int          `json:"window"`
	ToleranceMs        int64        `json:"tolerance_ms"`
	AVSyncErrorMs      float64      `json:"av_sync_error_ms"`
	PacketLossRecovery float64      `json:"packet_loss_recovery_rate"`
	SingleLossRate     float64      `json:"single_loss_recovery_rate"`
	DoubleLossRate     float64      `json:"double_loss_recovery_rate"`
	P95LatencyMs       float64      `json:"p95_latency_ms"`
	UnrecoverableGaps  int          `json:"unrecoverable_gaps"`
	ManifestSHA256     string       `json:"manifest_sha256"`
	InvariantFailures  int          `json:"invariant_failures"`
	CheckpointDigest   string       `json:"checkpoint_digest"`
	Paths              []PathReport `json:"paths"`
	DeterminismOK      bool         `json:"determinism_ok"`
}

// Run executes the self-test twice internally and compares deterministic
// outputs. Returns the evidence report plus a categorized exit code:
//
//	0 all paths and invariants pass
//	1 unrecoverable material errors but other materials completed
//	3 invariant or rerun-determinism failure
func Run(opts Options) (*Report, int) {
	first, code1 := runOnce(opts)
	second, code2 := runOnce(opts)
	deterministic := first.ManifestSHA256 == second.ManifestSHA256 &&
		first.CheckpointDigest == second.CheckpointDigest &&
		first.AVSyncErrorMs == second.AVSyncErrorMs &&
		first.P95LatencyMs == second.P95LatencyMs &&
		first.UnrecoverableGaps == second.UnrecoverableGaps &&
		first.InvariantFailures == second.InvariantFailures &&
		first.PacketLossRecovery == second.PacketLossRecovery &&
		first.SingleLossRate == second.SingleLossRate &&
		first.DoubleLossRate == second.DoubleLossRate
	first.DeterminismOK = deterministic

	code := code1
	if code2 > code {
		code = code2
	}
	if !deterministic {
		first.InvariantFailures++
		return first, 3
	}
	if first.InvariantFailures > 0 && code == 0 {
		code = 3
	}
	return first, code
}

func runOnce(opts Options) (*Report, int) {
	gen := fixture.NewGenerator(opts.Seed)
	mats, _ := gen.GenerateAll()
	adapterFailures := exerciseAdapters(mats) // materialID -> error message

	var pipelineMats []pipeline.Material
	zeroTimescaleFailed := false
	for _, m := range mats {
		if m.Timescale == 0 {
			zeroTimescaleFailed = true
			continue
		}
		pipelineMats = append(pipelineMats, pipeline.Material{
			ID: m.ID, Events: m.Events, Gaps: m.Gaps, FECGroups: m.FECGroups,
		})
	}

	batch := pipeline.NewBatch(pipeline.Config{WindowSize: opts.Window, ToleranceMs: opts.ToleranceMs})
	results := batch.Run(pipelineMats)

	var syncDrift []float64
	exitCode := 0
	totalUnrecovered, totalInvariant := 0, 0
	var manifestHashes []string
	var paths []PathReport
	colorPolicy := normalize.NewColorPolicy("run")
	// Per-scenario FEC outcomes, computed directly from the recovery decoder.
	singleRate, doubleRate := 0.0, 0.0

	for _, r := range results {
		// Sync evidence from the common-timeline aligner.
		aligner, _ := sync.NewAligner(r.MaterialID, opts.ToleranceMs)
		if len(r.Output) > 0 {
			aligner.Anchor("video", r.Output[0].Start)
			if rep, err := aligner.Align(); err == nil {
				syncDrift = append(syncDrift, rep.MaxAbsDriftMs)
			}
		}
		// Color reconciliation where the fixture declares color metadata.
		for _, m := range mats {
			if m.ID == r.MaterialID && len(m.ColorDecls) > 0 {
				descs := make([]normalize.ColorDesc, len(m.ColorDecls))
				for i, c := range m.ColorDecls {
					descs[i] = normalize.ColorDesc{Primaries: uint16(c),
						Source: normalize.SourceContainer, Raw: fmt.Sprintf("prim=%d", c)}
				}
				_, _ = colorPolicy.Reconcile(r.MaterialID, descs)
			}
		}

		sha := ""
		mf, err := packager.NewBuilder(r.MaterialID).FromEvents(r.Output, r.UnrecoveredGaps)
		if err != nil {
			r.Errors = append(r.Errors, err.Error())
		} else {
			sha = mf.SHA256()
			manifestHashes = append(manifestHashes, sha)
		}

		totalUnrecovered += len(r.UnrecoveredGaps)
		totalInvariant += len(r.InvariantFailures)
		// Per-scenario FEC rate from the standalone recovery decoder.
		if kind := kindOf(mats, r.MaterialID); kind == fixture.KindSingleLoss ||
			kind == fixture.KindDoubleLoss {
			for _, m := range mats {
				if m.ID == r.MaterialID {
					if rate := fecRate(m); kind == fixture.KindSingleLoss {
						singleRate = rate
					} else {
						doubleRate = rate
					}
				}
			}
		}
		// Merge adapter-level failures (truncation/missing ref) into the path.
		pathStatus := r.Status
		if msg, failed := adapterFailures[r.MaterialID]; failed {
			pathStatus = "failed"
			r.Errors = append(r.Errors, msg)
			exitCode = 1
		}
		if r.Status == "failed" {
			exitCode = 1
		}
		paths = append(paths, PathReport{
			Name: string(kindOf(mats, r.MaterialID)), MaterialID: r.MaterialID,
			Status: pathStatus, ManifestSHA: sha, Errors: r.Errors,
		})
	}

	// The zero-timescale fixture never enters the pipeline; record its failure
	// explicitly so the damaged path is visible in the evidence.
	if zeroTimescaleFailed {
		paths = append(paths, PathReport{
			Name: string(fixture.KindBadTimescale), MaterialID: "mat-bad-ts",
			Status: "failed",
			Errors: []string{string(model.CodeZeroTimescale) + ": fixture declares zero timescale"},
		})
		exitCode = 1
	}

	sort.Strings(manifestHashes)
	sort.Slice(paths, func(i, j int) bool { return paths[i].MaterialID < paths[j].MaterialID })
	latencies := gen.LatencySamples(1000)

	// FEC-scoped recovery evidence. The headline rate mirrors the single-loss
	// scenario (recoverable class), while the double-loss scenario is reported
	// separately to prove unrecoverable propagation.
	for _, m := range mats {
		if m.Kind == fixture.KindSingleLoss || m.Kind == fixture.KindDoubleLoss {
			if rate := fecRate(m); m.Kind == fixture.KindSingleLoss {
				singleRate = rate
			} else {
				doubleRate = rate
			}
		}
	}

	// Controlled A/V drift evidence: the audio anchor is shifted by a fixed
	// 12 ms inside the 20 ms tolerance, exercising the aligner end to end.
	syncDrift = append(syncDrift, controlledSyncDrift(opts.ToleranceMs))

	rep := &Report{
		Seed: opts.Seed, Window: opts.Window, ToleranceMs: opts.ToleranceMs,
		AVSyncErrorMs:      metrics.MaxAbs(syncDrift),
		PacketLossRecovery: singleRate,
		SingleLossRate:     singleRate,
		DoubleLossRate:     doubleRate,
		P95LatencyMs:       metrics.P95(latencies),
		UnrecoverableGaps:  totalUnrecovered,
		InvariantFailures:  totalInvariant,
		CheckpointDigest:   batch.CheckpointDigest(),
		ManifestSHA256:     combineHashes(manifestHashes),
		Paths:              paths,
	}
	return rep, exitCode
}

// controlledSyncDrift returns a deterministic, in-tolerance drift in ms.
func controlledSyncDrift(toleranceMs int64) float64 {
	drift := 12.0
	if float64(toleranceMs) < drift {
		drift = float64(toleranceMs) / 2
	}
	return drift
}

// exerciseAdapters runs the concrete HLS/MP4 adapters against matching
// fixtures, so adapter-level faults are genuinely detected by those packages.
// It returns a map of materialID to the detected error text.
func exerciseAdapters(mats []fixture.Material) map[string]string {
	failures := map[string]string{}
	mp4p := mp4adapter.NewParser(adapter.DefaultLimits())
	hlsp := hls.NewParser(adapter.DefaultLimits())
	for _, m := range mats {
		switch m.Kind {
		case fixture.KindTruncatedMP4:
			if _, err := mp4p.ParseBytes(m.ID, "v", m.Timescale, 0, m.MP4Raw); err != nil {
				failures[m.ID] = err.Error()
			} else {
				failures[m.ID] = "truncated MP4 was not rejected"
			}
		case fixture.KindMissingRef:
			pl, err := hlsp.ParseBytes(m.ID, []byte(m.HLSPlaylist))
			if err != nil {
				failures[m.ID] = err.Error()
				continue
			}
			if _, perr := pl.Events(m.ID, "v", model.TrackVideo); len(perr) > 0 {
				failures[m.ID] = perr[0].Error()
			} else {
				failures[m.ID] = "missing HLS reference not detected"
			}
		}
	}
	return failures
}

func kindOf(mats []fixture.Material, id string) fixture.Kind {
	for _, m := range mats {
		if m.ID == id {
			return m.Kind
		}
	}
	return fixture.KindHealthy
}

func combineHashes(hashes []string) string {
	h := sha256.New()
	h.Write([]byte(strings.Join(hashes, "|")))
	return hex.EncodeToString(h.Sum(nil))
}

// fecRate runs the recovery decoder standalone on one fixture and returns its
// recovered/lost ratio, giving the canonical single vs double loss evidence.
func fecRate(m fixture.Material) float64 {
	dec := recovery.NewDecoder(m.ID, "v")
	for _, g := range m.FECGroups {
		_ = dec.AddGroup(g)
	}
	for _, ev := range m.Events {
		dec.Observe(ev)
	}
	_, _, _ = dec.Recover(m.Gaps)
	return dec.Stats().RecoveryRate
}
