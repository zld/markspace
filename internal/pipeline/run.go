package pipeline

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/zld/media-check/internal/diagnostic"
	"github.com/zld/media-check/internal/export"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/fingerprint"
	"github.com/zld/media-check/internal/fixture"
	"github.com/zld/media-check/internal/ingest"
	"github.com/zld/media-check/internal/jobs"
	"github.com/zld/media-check/internal/manifest"
	"github.com/zld/media-check/internal/mp4"
	"github.com/zld/media-check/internal/stream"
	"github.com/zld/media-check/internal/timeline"
)

const InputRelPath = "input/media-001.mp4"

func Run(cfg Config) (*Outcome, error) {
	if cfg.WorkDir == "" {
		return &Outcome{ExitCode: ExitUsage}, fault.E(fault.StageJobs, fault.CodeInvalidArgument, "--work is required")
	}
	if cfg.Seed == 0 {
		cfg.Seed = 17
	}
	if cfg.TaskKey == "" {
		variant := cfg.Corrupt
		if variant == "" {
			variant = "normal"
		}
		cfg.TaskKey = fingerprint.StableTaskKey(
			fmt.Sprintf("media-check-demo/v1/seed=%d/variant=%s", cfg.Seed, variant), nil)
	}
	out := &Outcome{TaskKey: cfg.TaskKey, WorkDir: cfg.WorkDir}

	store, err := jobs.NewStore(cfg.WorkDir)
	if err != nil {
		out.ExitCode = ExitState
		return out, err
	}

	var st *State
	var data []byte

	if cfg.Resume {
		absInput := filepath.Join(cfg.WorkDir, filepath.FromSlash(InputRelPath))
		hash, err := fingerprint.File(absInput)
		if err != nil {
			out.ExitCode = ExitState
			return out, fault.Wrap(fault.StageJobs, fault.CodeReadFailed, err,
				"hash input before resume", fault.WithPath(absInput))
		}
		cp, err := store.ResumeCheckpoint(cfg.TaskKey, hash.SHA256)
		if err != nil {
			out.ExitCode = ExitState
			return out, err
		}
		st = &State{}
		if len(cp.State) > 0 {
			if err := json.Unmarshal(cp.State, st); err != nil {
				out.ExitCode = ExitState
				return out, fault.Wrap(fault.StageJobs, fault.CodeInvalidArgument, err, "decode checkpoint state")
			}
		}
		if st.Scan == nil || st.MP4 == nil {
			out.ExitCode = ExitState
			return out, fault.E(fault.StageJobs, fault.CodeCheckpointNotFound, "checkpoint does not contain scan state")
		}
		st.Config.Resume = true
		st.Config.WorkDir = cfg.WorkDir
		out.Resumed = true
		out.ResumeFrom = cp.LastStage
		src, err := ingest.ReadFile(absInput, ingest.ReadOptions{ID: InputRelPath})
		if err != nil {
			out.ExitCode = ExitState
			return out, err
		}
		data = src.Data
	} else {
		fx, err := fixture.Generate(fixture.Config{Seed: cfg.Seed, Corrupt: cfg.Corrupt})
		if err != nil {
			out.ExitCode = ExitUsage
			return out, err
		}
		inputPath, _, err := fixture.WriteSet(cfg.WorkDir, fx)
		if err != nil {
			out.ExitCode = ExitState
			return out, err
		}
		src, err := ingest.ReadFile(inputPath, ingest.ReadOptions{ID: InputRelPath})
		if err != nil {
			out.ExitCode = ExitState
			return out, err
		}
		data = src.Data
		parsed := mp4.Parse(data, InputRelPath, src.Hash.SHA256)
		scanRes := stream.Scan(InputRelPath, src.Hash.SHA256, data, parsed, stream.DefaultMaxKeyframeIntervalMS)
		st = &State{
			Config:       cfg,
			TaskKey:      cfg.TaskKey,
			InputRelPath: InputRelPath,
			Input: manifest.InputRecord{
				ID: InputRelPath, Path: InputRelPath, Size: src.Size,
				Fingerprint: src.Hash, Role: "synthetic-media",
			},
			MP4:    parsed.Container.Manifest,
			Parse:  parsed,
			Scan:   scanRes,
			Stages: []string{"ingest", "mp4", "scan"},
		}
		if err := persist(store, st, "scan", "scan-complete"); err != nil {
			out.ExitCode = ExitState
			return out, err
		}
		if cfg.InterruptAfter == "scan" {
			if err := persist(store, st, "scan", "interrupted"); err != nil {
				out.ExitCode = ExitState
				return out, err
			}
			out.Interrupted = true
			out.Job, _ = store.LoadJob(cfg.TaskKey)
			return out, nil
		}
		if cfg.InterruptAfter != "" {
			out.ExitCode = ExitUsage
			return out, fault.E(fault.StageJobs, fault.CodeInvalidArgument,
				"only --interrupt-after=scan is supported")
		}
	}

	if st.Timeline == nil {
		st.Timeline = timeline.Assemble(st.MP4, st.Scan)
		appendStage(&st.Stages, "timeline")
		if err := persist(store, st, "timeline", "running"); err != nil {
			out.ExitCode = ExitState
			return out, err
		}
	}
	if st.Diagnostic == nil {
		st.Diagnostic = diagnostic.Locate(st.Scan, st.Timeline)
		appendStage(&st.Stages, "diagnostic")
		if err := persist(store, st, "diagnostic", "running"); err != nil {
			out.ExitCode = ExitState
			return out, err
		}
	}
	out.Damage = st.Diagnostic

	var exportIssues []*fault.Error
	if st.Diagnostic.DamageCount == 0 && st.Export == nil {
		st.Export, exportIssues = export.Run(export.Options{
			Data: data, InputID: InputRelPath, InputSHA: st.Input.Fingerprint.SHA256,
			OutDir: cfg.WorkDir, Manifest: st.MP4, Scan: st.Scan, Timeline: st.Timeline,
		})
		appendStage(&st.Stages, "export")
		if err := persist(store, st, "export", "running"); err != nil {
			out.ExitCode = ExitState
			return out, err
		}
	}

	allIssues := []*fault.Error{}
	allIssues = append(allIssues, st.Scan.Issues...)
	allIssues = append(allIssues, st.Timeline.Issues...)
	allIssues = append(allIssues, exportIssues...)

	appendStage(&st.Stages, "manifest")
	m := buildManifest(st, allIssues)
	if existing, ok, err := manifest.Load(filepath.Join(cfg.WorkDir, "manifest.json")); err != nil {
		out.ExitCode = ExitState
		return out, err
	} else if ok && existing.TaskKey == m.TaskKey {
		m = manifest.Merge(existing, m)
	}
	// A root manifest belonging to another task key is replaced as the
	// work-directory pointer. Its task-keyed archive copy remains intact.
	rootPath, archivePath, sum, err := manifest.Archive(cfg.WorkDir, m)
	if err != nil {
		out.ExitCode = ExitState
		return out, err
	}
	st.Manifest = m
	if err := persist(store, st, "manifest", statusFor(st.Diagnostic, exportIssues)); err != nil {
		out.ExitCode = ExitState
		return out, err
	}

	out.Manifest = m
	out.ManifestPath = rootPath
	out.ArchivePath = archivePath
	out.ManifestSHA256 = sum
	out.Job, _ = store.LoadJob(cfg.TaskKey)
	if st.Diagnostic.DamageCount > 0 || len(exportIssues) > 0 {
		out.ExitCode = ExitDamage
		return out, nil
	}
	out.ExitCode = ExitOK
	return out, nil
}

func appendStage(stages *[]string, stage string) {
	for _, s := range *stages {
		if s == stage {
			return
		}
	}
	*stages = append(*stages, stage)
}

func persist(store *jobs.Store, st *State, lastStage, status string) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return fault.Wrap(fault.StageJobs, fault.CodeInvalidArgument, err, "encode checkpoint state")
	}
	cp := jobs.Checkpoint{
		TaskKey:          st.TaskKey,
		InputFingerprint: st.Input.Fingerprint.SHA256,
		LastStage:        lastStage,
		CompletedStages:  append([]string(nil), st.Stages...),
		State:            raw,
	}
	if err := store.SaveCheckpoint(cp); err != nil {
		return err
	}
	manifestSHA := ""
	if st.Manifest != nil {
		if _, sum, err := manifest.Encode(st.Manifest); err == nil {
			manifestSHA = sum
		}
	}
	return store.SaveJob(jobs.JobRecord{
		TaskKey:          st.TaskKey,
		Scope:            "media-check-demo",
		InputFingerprint: st.Input.Fingerprint.SHA256,
		Status:           status,
		CompletedStages:  append([]string(nil), st.Stages...),
		ManifestSHA256:   manifestSHA,
	})
}

func statusFor(diag *diagnostic.Report, exportIssues []*fault.Error) string {
	if diag != nil && diag.DamageCount > 0 {
		return "damaged"
	}
	if len(exportIssues) > 0 {
		return "failed"
	}
	return "completed"
}
