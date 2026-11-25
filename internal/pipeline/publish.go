package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"regress-kernel/internal/media"
)

// segmentPath locates one rendition's fragment inside the workspace.
func (w *Workspace) segmentPath(taskKey, name string) (string, error) {
	return w.SafeJoin("segments", sanitizeKey(taskKey), name)
}

// PublishOptions controls one idempotent publish run.
type PublishOptions struct {
	TaskKey   string
	Rendition string
	Frames    []media.Frame
	Clock     media.Clock
	// CrashAfterWrite simulates an exit after bytes are durable but before the
	// task state is committed, exercising the recover-only path on rerun.
	CrashAfterWrite bool
}

// Publish renders and durably writes every fragment, then returns the
// artifact records. It is idempotent across reruns:
//
//   - record present and file matching  -> reuse, nothing is rewritten;
//   - file present but unrecorded       -> verify digest, adopt the artifact;
//   - file present with a wrong digest  -> quarantine + ErrDigestMismatch;
//   - file absent                        -> render and atomically write once.
func (w *Workspace) Publish(opts PublishOptions) ([]ArtifactRecord, bool, error) {
	groups := SegmentFrames(opts.Frames)
	records := make([]ArtifactRecord, 0, len(groups))
	preExisting := map[string]bool{}

	for i, group := range groups {
		name := fmt.Sprintf("seg-%05d.m4s", i+1)
		raw, err := RenderSegment(uint32(i+1), group[0].TimestampTicks, group)
		if err != nil {
			return nil, false, err
		}
		digest := FileSHA256(raw)
		path, err := w.segmentPath(opts.TaskKey, name)
		if err != nil {
			return nil, false, err
		}
		rec := ArtifactRecord{
			Name:      name,
			StartTick: group[0].TimestampTicks,
			Size:      int64(len(raw)),
			SHA256:    digest,
		}

		onDisk, readErr := os.ReadFile(path)
		switch {
		case readErr == nil:
			// Artifact survived a crash before state commit: adopt only when
			// the content digest proves it is the same publish.
			if FileSHA256(onDisk) != digest {
				return nil, false, w.quarantineOnMismatch(opts, name, digest, FileSHA256(onDisk))
			}
			preExisting[name] = true
		case os.IsNotExist(readErr):
			if err := w.atomicWrite(path, raw); err != nil {
				return nil, false, err
			}
		default:
			return nil, false, readErr
		}
		records = append(records, rec)
	}

	if opts.CrashAfterWrite {
		// Model the process exiting between durable write and state commit.
		return records, len(preExisting) == len(records), ErrSimulatedCrash
	}
	return records, len(preExisting) == len(records), nil
}

// ErrSimulatedCrash marks the deliberate pre-commit exit used by tests.
var ErrSimulatedCrash = fmt.Errorf("simulated crash before state commit")

// atomicWrite writes via a sibling temp file and renames into place, so a
// partial segment can never be observed by a recovery run.
func (w *Workspace) atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (w *Workspace) quarantineOnMismatch(opts PublishOptions,
	name, want, got string) error {
	st := &TaskState{
		TaskKey:   opts.TaskKey,
		Rendition: opts.Rendition,
		Status:    StateFailed,
		Stage:     StagePublish,
		Failure: fmt.Sprintf("%s: %s want=%s got=%s",
			media.ErrDigestMismatch, name, want[:12], got[:12]),
	}
	if _, err := w.Quarantine(st, st.Failure); err != nil {
		return err
	}
	return fmt.Errorf("%w: task %s segment %s",
		media.ErrDigestMismatch, opts.TaskKey, name)
}

// VerifyArtifacts re-hashes every recorded segment from disk, proving the
// published set still matches the state that describes it.
func (w *Workspace) VerifyArtifacts(taskKey string,
	records []ArtifactRecord) error {
	for _, rec := range records {
		path, err := w.segmentPath(taskKey, rec.Name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if FileSHA256(data) != rec.SHA256 || int64(len(data)) != rec.Size {
			return fmt.Errorf("%w: %s", media.ErrDigestMismatch, rec.Name)
		}
	}
	return nil
}
