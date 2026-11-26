package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/checkpoint"
	"mediasync/internal/ingest"
	"mediasync/internal/media"
	"mediasync/internal/output"
	"mediasync/internal/render"
	"mediasync/internal/timeline"
)

type Options struct {
	InputDir         string
	OutDir           string
	BudgetUnits      int64
	MaxBytes         int64
	CancelAfterUnits int64
	Resume           bool
}

type Report struct {
	State          checkpoint.State `json:"state"`
	Tracks         int              `json:"tracks"`
	Unique         int              `json:"unique"`
	Duplicates     int              `json:"duplicates"`
	Gaps           int              `json:"gaps"`
	Loss           string           `json:"loss"`
	Frames         int              `json:"frames"`
	Files          int              `json:"files"`
	Bytes          int64            `json:"bytes"`
	RootHash       string           `json:"root_hash,omitempty"`
	UsedUnits      int64            `json:"used_units"`
	BudgetUnits    int64            `json:"budget_units"`
	ResumeTrack    string           `json:"resume_track,omitempty"`
	ResumeRecord   int              `json:"resume_record"`
	NextRecord     int              `json:"next_record"`
	CheckpointPath string           `json:"checkpoint_path"`
}

func Run(ctx context.Context, opts Options) (*Report, error) {
	rep := &Report{BudgetUnits: opts.BudgetUnits, CheckpointPath: checkpoint.Path(opts.OutDir)}
	if opts.InputDir == "" || opts.OutDir == "" {
		return rep, media.InputError("cli", "", errors.New("--input and --out are required"))
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return rep, media.InputError("pipeline.init", "", err)
	}

	cp := checkpoint.New()
	if opts.Resume {
		existing, err := checkpoint.Load(opts.OutDir)
		if err != nil {
			return rep, media.InputError("checkpoint.load", "", err)
		}
		if existing == nil {
			return rep, media.InputError("checkpoint.load", "", fmt.Errorf("no checkpoint in %s", opts.OutDir))
		}
		cp = existing
		if cp.State == checkpoint.StateCommitted {
			cat, _, err := output.LoadCatalog(opts.OutDir)
			if err != nil {
				return rep, err
			}
			fillCommittedReport(rep, cat, cp.CommittedUnits)
			return rep, nil
		}
	} else {
		_ = os.Remove(filepath.Join(opts.OutDir, output.CatalogName))
	}
	rep.State = cp.State

	manifest, _, err := ingest.LoadManifest(opts.InputDir)
	if err != nil {
		return rep, err
	}
	cp.State = checkpoint.StateScanning
	if err := cp.Save(opts.OutDir); err != nil {
		return rep, media.InputError("checkpoint.save", "", err)
	}

	loadedTracks := make([]loaded, 0, len(manifest.Tracks))
	inputs := make([]timeline.Input, 0, len(manifest.Tracks))
	for _, spec := range manifest.Tracks {
		lt, err := loadTrack(spec, opts.InputDir)
		if err != nil {
			return rep, err
		}
		loadedTracks = append(loadedTracks, lt)
		inputs = append(inputs, lt.input)
	}

	resumeIndex := -1
	for i, spec := range manifest.Tracks {
		if cp.LastCommittedTrack == spec.ID {
			resumeIndex = i
		}
	}
	units := cp.CommittedUnits
	rep.UsedUnits = units
	for i, lt := range loadedTracks {
		count := lt.track.recordCount()
		start := 0
		if i < resumeIndex {
			start = count
		} else if i == resumeIndex {
			start = cp.LastRecordPosition + 1
			if start > count {
				start = count
			}
		}
		for pos := start; pos < count; pos++ {
			if err := interruption(ctx, opts, cp, rep, units, "scan"); err != nil {
				return rep, err
			}
			units++
			cp.CommitPosition(lt.spec.ID, pos, units)
			if err := cp.Save(opts.OutDir); err != nil {
				return rep, media.InputError("checkpoint.save", lt.spec.ID, err).WithRecord(pos)
			}
			rep.UsedUnits = units
		}
	}

	cp.State = checkpoint.StateMerging
	if err := cp.Save(opts.OutDir); err != nil {
		return rep, media.InputError("checkpoint.save", "", err)
	}
	tl, err := timeline.Merge(inputs)
	if err != nil {
		return rep, err
	}
	rep.Tracks = tl.Stats.Tracks
	rep.Unique = tl.Stats.Unique
	rep.Duplicates = tl.Stats.Duplicates
	rep.Gaps = tl.Stats.Gaps
	rep.Loss = tl.Stats.LossPercent
	if err := tl.CheckQuality(); err != nil {
		_ = cp.Save(opts.OutDir)
		return rep, err
	}
	if opts.BudgetUnits > 0 && units > opts.BudgetUnits {
		return pauseBudget(rep, cp, opts.OutDir, "budget-units",
			fmt.Errorf("used %d units exceeds budget %d", units, opts.BudgetUnits))
	}

	cp.State = checkpoint.StateRendering
	if err := cp.Save(opts.OutDir); err != nil {
		return rep, media.InputError("checkpoint.save", "", err)
	}
	specs, err := render.BuildSpecs(tl, manifest.FrameTimesMS)
	if err != nil {
		return rep, err
	}
	frames, err := render.RenderAll(ctx, specs, opts.OutDir)
	if err != nil {
		if ctx.Err() != nil {
			return pauseCanceled(rep, cp, opts.OutDir, "signal")
		}
		return rep, media.InputError("render", "", err)
	}
	timelineEntry, err := output.WriteTimeline(opts.OutDir, tl)
	if err != nil {
		return rep, err
	}

	budget := output.Budget{BudgetUnits: opts.BudgetUnits, UsedUnits: units, MaxBytes: opts.MaxBytes}
	catalog := output.BuildCatalog(tl, frames, []output.FileEntry{timelineEntry}, budget)
	catalogBytes, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return rep, err
	}
	if opts.MaxBytes > 0 && catalog.TotalBytes+int64(len(catalogBytes)) > opts.MaxBytes {
		return pauseBudget(rep, cp, opts.OutDir, "max-bytes",
			fmt.Errorf("delivery %d bytes exceeds --max-bytes %d", catalog.TotalBytes+int64(len(catalogBytes)), opts.MaxBytes))
	}
	if err := output.SaveCatalog(opts.OutDir, catalog); err != nil {
		return rep, err
	}
	cp.State = checkpoint.StateCommitted
	cp.RootHash = catalog.RootHash
	cp.CancelReason = ""
	if err := cp.Save(opts.OutDir); err != nil {
		return rep, media.InputError("checkpoint.save", "", err)
	}
	fillCommittedReport(rep, catalog, units)
	return rep, nil
}

func interruption(ctx context.Context, opts Options, cp *checkpoint.Checkpoint, rep *Report, units int64, phase string) error {
	if ctx.Err() != nil {
		_, err := pauseCanceled(rep, cp, opts.OutDir, "signal")
		return err
	}
	if opts.CancelAfterUnits > 0 && units >= opts.CancelAfterUnits {
		_, err := pauseCanceled(rep, cp, opts.OutDir, "cancel-after-units")
		return err
	}
	return nil
}

func pauseCanceled(rep *Report, cp *checkpoint.Checkpoint, outDir, reason string) (*Report, error) {
	cp.Pause(reason)
	if err := cp.Save(outDir); err != nil {
		return rep, media.InputError("checkpoint.save", cp.LastCommittedTrack, err)
	}
	fillPausedReport(rep, cp)
	return rep, media.CanceledError("pipeline."+reason, cp.LastCommittedTrack)
}

func pauseBudget(rep *Report, cp *checkpoint.Checkpoint, outDir, reason string, cause error) (*Report, error) {
	cp.Pause(reason)
	if err := cp.Save(outDir); err != nil {
		return rep, media.InputError("checkpoint.save", cp.LastCommittedTrack, err)
	}
	fillPausedReport(rep, cp)
	return rep, media.BudgetError("pipeline."+reason, cause)
}

func fillPausedReport(rep *Report, cp *checkpoint.Checkpoint) {
	rep.State = checkpoint.StatePaused
	rep.ResumeTrack = cp.LastCommittedTrack
	rep.ResumeRecord = cp.LastRecordPosition
	rep.NextRecord = cp.LastRecordPosition + 1
	rep.UsedUnits = cp.CommittedUnits
}

func fillCommittedReport(rep *Report, c *output.Catalog, units int64) {
	rep.State = checkpoint.StateCommitted
	rep.Tracks = c.Stats.Tracks
	rep.Unique = c.Stats.Unique
	rep.Duplicates = c.Stats.Duplicates
	rep.Gaps = c.Stats.Gaps
	rep.Loss = c.Stats.LossPercent
	rep.Frames = c.Frames
	rep.Files = c.FileCount
	rep.Bytes = c.TotalBytes
	rep.RootHash = c.RootHash
	rep.UsedUnits = units
	rep.ResumeTrack = ""
	rep.ResumeRecord = 0
	rep.NextRecord = 0
}
