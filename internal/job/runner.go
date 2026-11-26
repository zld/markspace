package job

import (
	"fmt"
	"time"

	"mediad/internal/manifest"
	"mediad/internal/media"
)

// Config bundles the external collaborators a run needs.
type Config struct {
	Store        *Store
	Archive      *Archive
	Timeline     *media.Timeline
	SourceDigest string
	PolicyDigest string
	// PauseAfterSample, when > 0, stops the run once this many samples are
	// archived, leaving the task in the archive phase for a later resume.
	PauseAfterSample int64
}

// RunResult summarizes what one invocation did, for stable CLI output.
type RunResult struct {
	TaskKey           string
	ResumedFromSample int64
	DuplicateWrites   int
	NewWrites         int
	TotalSamples      int64
	Paused            bool
	ManifestDigest    string
	Entries           []manifest.Entry
}

// Runner executes the state machine for one task.
type Runner struct{ cfg Config }

// NewRunner validates the collaborators.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.Store == nil || cfg.Archive == nil || cfg.Timeline == nil {
		return nil, fmt.Errorf("job: runner requires store, archive and timeline")
	}
	if cfg.SourceDigest == "" || cfg.PolicyDigest == "" {
		return nil, fmt.Errorf("job: source and policy digests are mandatory")
	}
	return &Runner{cfg: cfg}, nil
}

// Run executes until completion, an input error, or the configured pause
// point. Re-invoking Run on a paused task resumes at the next sample.
func (r *Runner) Run() (*RunResult, error) {
	ordered := r.cfg.Timeline.OrderedSamples()
	total := int64(len(ordered))
	key := TaskKey(r.cfg.SourceDigest, r.cfg.PolicyDigest)
	res := &RunResult{TaskKey: key, TotalSamples: total}

	task, err := r.cfg.Store.GetTask(key)
	alreadyComplete := false
	if err != nil {
		task = &Task{
			Key: key, SourceDigest: r.cfg.SourceDigest,
			PolicyDigest: r.cfg.PolicyDigest, TotalSamples: total,
		}
		if _, uerr := r.cfg.Store.UpsertTask(task); uerr != nil {
			return nil, uerr
		}
	} else if task.Phase == PhaseComplete {
		alreadyComplete = true
	}

	// Advance the lightweight pre-archive phases if this is a fresh task.
	if task.Phase == PhaseQueued {
		if err := r.runPreArchivePhases(key); err != nil {
			return nil, err
		}
	}

	start, err := r.cfg.Store.LatestCheckpoint(key)
	if err != nil {
		return nil, err
	}
	res.ResumedFromSample = start

	for i := start; i < total; i++ {
		item := ordered[i]
		exists, _, err := r.cfg.Store.ObjectExists(key, item.Track, item.Sample.Index)
		if err != nil {
			return nil, err
		}
		var payload []byte
		if item.Sample.Payload != nil {
			payload = item.Sample.Payload
		} else {
			// MP4 samples carry no in-memory payload; synthesize deterministic
			// bytes from their identity so content addressing stays testable.
			payload = syntheticPayload(item.Track, item.Sample)
		}
		if exists {
			res.DuplicateWrites++
		} else {
			hash, written, perr := r.cfg.Archive.Put(payload)
			if perr != nil {
				return nil, media.NewError(media.StageArchive, "archive.put", perr.Error()).
					WithTrack(item.Track).WithOffset(item.Sample.DataOffset)
			}
			if written {
				res.NewWrites++
			} else {
				res.DuplicateWrites++
			}
			if err := r.cfg.Store.RecordObject(key, item.Track, item.Sample.Index,
				hash, item.Sample.Size); err != nil {
				return nil, err
			}
		}
		// Checkpoint after every sample so a crash loses at most one unit.
		if err := r.cfg.Store.SaveCheckpoint(Checkpoint{
			TaskKey: key, Phase: PhaseArchive,
			LastSample: i + 1, UpdatedAt: time.Now().UnixNano(),
		}); err != nil {
			return nil, err
		}
		if r.cfg.PauseAfterSample > 0 && i+1 >= r.cfg.PauseAfterSample && i+1 < total {
			res.Paused = true
			return res, nil
		}
	}

	if err := r.publish(key, res, alreadyComplete); err != nil {
		return nil, err
	}
	return res, nil
}

// runPreArchivePhases walks scan -> fingerprint -> plan, checkpointing each.
func (r *Runner) runPreArchivePhases(key string) error {
	from := PhaseQueued
	for _, to := range []Phase{PhaseScan, PhaseFingerprint, PhasePlan} {
		if err := r.cfg.Store.Advance(key, from, to); err != nil {
			return err
		}
		if err := r.cfg.Store.SaveCheckpoint(Checkpoint{
			TaskKey: key, Phase: to, LastSample: 0,
			UpdatedAt: time.Now().UnixNano(),
		}); err != nil {
			return err
		}
		from = to
	}
	return r.cfg.Store.Advance(key, PhasePlan, PhaseArchive)
}

// publish verifies every object, builds the manifest, and only then marks
// the task complete. A verification failure aborts before any digest is set.
// An already-complete task is re-verified idempotently without a forbidden
// phase transition.
func (r *Runner) publish(key string, res *RunResult, alreadyComplete bool) error {
	objs, err := r.cfg.Store.Objects(key)
	if err != nil {
		return err
	}
	entries := make([]manifest.Entry, 0, len(objs))
	for _, o := range objs {
		if err := r.cfg.Archive.Verify(o.Hash); err != nil {
			_ = r.cfg.Store.RecordFailure(key, string(media.StageRecover), -1, err.Error())
			return media.NewError(media.StageRecover, "archive.verify", err.Error())
		}
		entries = append(entries, manifest.Entry{
			TaskKey: key, TrackKey: o.TrackKey, SampleIdx: o.SampleIdx,
			ObjectHash: o.Hash, Size: o.Size,
		})
	}
	merged, err := manifest.Merge(entries)
	if err != nil {
		return media.NewError(media.StageArchive, "manifest.merge", err.Error())
	}
	digest := manifest.Digest(merged)
	if err := r.cfg.Store.SetManifest(key, digest); err != nil {
		return err
	}
	if !alreadyComplete {
		if err := r.cfg.Store.Advance(key, PhaseArchive, PhaseComplete); err != nil {
			return err
		}
	}
	res.Entries = merged
	res.ManifestDigest = digest
	return nil
}

// syntheticPayload produces deterministic content for samples whose source
// payload was not retained in memory (streamed MP4 inputs).
func syntheticPayload(track string, s media.Sample) []byte {
	return []byte(fmt.Sprintf("%s:%d:%d:%d", track, s.Index, s.StartNs, s.Size))
}
