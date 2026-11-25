package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"rtpregress/internal/media"
)

// Options configure a run.
type Options struct {
	// Dir is the output directory. All stage outputs, the manifest and the
	// summary live inside it.
	Dir string
	// IdemKey identifies the logical run.
	IdemKey string
	// InputDigest hashes the run's inputs.
	InputDigest string
	// ParamsDigest hashes the effective parameters.
	ParamsDigest string
	// InterruptAfter names a stage after whose commit the run stops.
	InterruptAfter string
	// ManifestName is the manifest filename (default manifest.json).
	ManifestName string
	// SummaryName is the summary filename (default summary.json).
	SummaryName string
}

// Runner executes stages in order, persisting each stage's output and the
// manifest commit point.
type Runner struct {
	opt    Options
	stages map[string]StageFunc
	mu     sync.Mutex
}

// NewRunner builds a runner for the given stage functions.
func NewRunner(opt Options, stages map[string]StageFunc) *Runner {
	if opt.ManifestName == "" {
		opt.ManifestName = "manifest.json"
	}
	if opt.SummaryName == "" {
		opt.SummaryName = "summary.json"
	}
	return &Runner{opt: opt, stages: stages}
}

// ManifestPath returns the manifest location.
func (r *Runner) ManifestPath() string { return filepath.Join(r.opt.Dir, r.opt.ManifestName) }

// SummaryPath returns the summary location.
func (r *Runner) SummaryPath() string { return filepath.Join(r.opt.Dir, r.opt.SummaryName) }

// StagePath returns where a stage's output is stored.
func (r *Runner) StagePath(stage string) string { return stagePath(r.opt.Dir, stage) }

// Run executes the pipeline, resuming after the last committed stage.
//
// The lock file makes the directory single-writer: a concurrent delivery of
// the same key either waits its turn or sees the completed manifest, and can
// never observe a half-written manifest because the manifest is replaced by
// an atomic rename.
//
// Stages are committed in order. On resume a stage is skipped only when the
// manifest records it and its persisted output still hashes to the recorded
// digest; any missing or altered output is recomputed, so a crash between
// writing a stage file and committing the manifest can never publish a
// digest that does not match its bytes.
func (r *Runner) Run(summary RunSummary) (*Manifest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := os.MkdirAll(r.opt.Dir, 0o755); err != nil {
		return nil, media.Newf(media.KindResume, "create output dir: %v", err)
	}
	unlock, err := acquireLock(filepath.Join(r.opt.Dir, ".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()

	m, err := r.loadOrInit()
	if err != nil {
		return nil, err
	}
	if err := r.checkIdempotency(m); err != nil {
		return nil, err
	}
	if m.Completed {
		// Identical re-delivery of a finished run is a no-op; the caller
		// still re-verifies the artifact it reads back.
		return m, nil
	}

	// The committed prefix is rebuilt by walking the stage order and keeping
	// records whose output bytes still hash as recorded.
	committed := make([]StageRecord, 0, len(Stages))
	for _, name := range Stages {
		rec, ok := findStage(m, name)
		if !ok {
			break
		}
		if !r.outputIntact(rec) {
			break
		}
		committed = append(committed, rec)
	}
	m.Stages = committed
	start := len(committed)
	m.Summary = summary
	m.Summary.Resumed = start > 0

	for i := start; i < len(Stages); i++ {
		name := Stages[i]
		fn, ok := r.stages[name]
		if !ok {
			return nil, media.Newf(media.KindResume, "no implementation for stage %q", name)
		}
		prior := ""
		if i > 0 {
			prior = m.Stages[i-1].OutputDigest
		}
		out, err := fn(StageInput{
			Stage: name, InputDigest: r.opt.InputDigest,
			ParamsDigest: r.opt.ParamsDigest, Dir: r.opt.Dir, Prior: prior,
		})
		if err != nil {
			return nil, media.At(err, media.Loc{Stage: name})
		}
		// Persist the bytes first, then the record. If we crash between the
		// two, the next run recomputes the stage: the digest in the manifest
		// is the authority.
		if err := AtomicWrite(r.StagePath(name), out.Bytes); err != nil {
			return nil, err
		}
		rec := StageRecord{
			Name: name, InputDigest: r.opt.InputDigest,
			OutputDigest: Digest(out.Bytes), Attempt: 1,
		}
		// A resumed stage that sees the same input digest keeps its attempt
		// count, so resumed and continuous runs produce the same manifest.
		if prev, ok := findStage(m, name); ok && prev.InputDigest == rec.InputDigest {
			rec.Attempt = prev.Attempt
		}
		m.Stages = append(m.Stages[:i], rec)
		if err := r.persist(m); err != nil {
			return nil, err
		}
		if r.opt.InterruptAfter == name {
			return nil, media.At(
				media.Newf(media.KindInterrupt, "interrupted after stage %q by request", name),
				media.Loc{Stage: name})
		}
	}

	// The run is complete once all stages are committed. Completion is
	// published in the same atomic write that carries the last stage record.
	m.Completed = true
	if err := r.persist(m); err != nil {
		return nil, err
	}
	return m, nil
}

// outputIntact reports whether a committed stage's persisted bytes still hash
// to the digest recorded in the manifest. A missing or altered file means the
// stage must run again.
func (r *Runner) outputIntact(rec StageRecord) bool {
	data, err := os.ReadFile(r.StagePath(rec.Name))
	if err != nil {
		return false
	}
	return Digest(data) == rec.OutputDigest
}

// MarkCompleted records the final artifact and summary and re-publishes the
// manifest atomically. It exists for callers whose artifact is produced after
// the stage loop (for example by the verify stage), so the published manifest
// always describes a finished run.
func (r *Runner) MarkCompleted(m *Manifest, artifact ArtifactRecord, s RunSummary) (*Manifest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m.Artifact = artifact
	m.Summary = s
	m.Summary.Resumed = m.Summary.Resumed || len(m.Stages) < len(Stages)
	m.Completed = true
	if err := r.persist(m); err != nil {
		return nil, err
	}
	return m, nil
}

func findStage(m *Manifest, name string) (StageRecord, bool) {
	for _, rec := range m.Stages {
		if rec.Name == name {
			return rec, true
		}
	}
	return StageRecord{}, false
}

func (r *Runner) loadOrInit() (*Manifest, error) {
	m, err := LoadManifest(r.ManifestPath())
	if err != nil {
		if errors.Is(errors.Unwrap(err), os.ErrNotExist) {
			return &Manifest{
				Version: ManifestVersion, IdemKey: r.opt.IdemKey,
				InputDigest: r.opt.InputDigest, ParamsDigest: r.opt.ParamsDigest,
				Stages: []StageRecord{},
			}, nil
		}
		return nil, err
	}
	return m, nil
}

func (r *Runner) checkIdempotency(m *Manifest) error {
	if m.IdemKey == "" {
		m.IdemKey = r.opt.IdemKey
	}
	if m.InputDigest == "" {
		m.InputDigest = r.opt.InputDigest
	}
	if m.ParamsDigest == "" {
		m.ParamsDigest = r.opt.ParamsDigest
	}
	if m.IdemKey != r.opt.IdemKey {
		return media.Newf(media.KindConflict,
			"output directory holds idempotency key %q, not %q", m.IdemKey, r.opt.IdemKey)
	}
	if m.InputDigest != r.opt.InputDigest || m.ParamsDigest != r.opt.ParamsDigest {
		return ConflictError(m.IdemKey, m.InputDigest, r.opt.InputDigest,
			m.ParamsDigest, r.opt.ParamsDigest)
	}
	return nil
}

// persist commits the manifest by atomic replacement.
func (r *Runner) persist(m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return media.Newf(media.KindResume, "marshal manifest: %v", err)
	}
	if err := AtomicWrite(r.ManifestPath(), data); err != nil {
		return err
	}
	summary, err := json.MarshalIndent(m.Summary, "", "  ")
	if err != nil {
		return media.Newf(media.KindResume, "marshal summary: %v", err)
	}
	return AtomicWrite(r.SummaryPath(), summary)
}

// acquireLock creates an exclusive lock file. Stale locks are not reclaimed:
// an operator removing a crashed run's directory or lock file is explicit.
func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, media.Newf(media.KindResume,
				"output %s is locked by another run", filepath.Dir(path))
		}
		return nil, media.Newf(media.KindResume, "create lock: %v", err)
	}
	f.Close()
	return func() { os.Remove(path) }, nil
}

// Describe renders a stage record for terminal output.
func (r StageRecord) Describe() string {
	return fmt.Sprintf("%s input=%s output=%s", r.Name,
		shortDigest(r.InputDigest), shortDigest(r.OutputDigest))
}

func shortDigest(d string) string {
	const prefix = "sha256:"
	if len(d) > len(prefix) && d[:len(prefix)] == prefix {
		d = d[len(prefix):]
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
