package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"continuity/internal/cache"
	"continuity/internal/fixture"
	"continuity/internal/journal"
	"continuity/internal/model"
	"continuity/internal/pipeline"
	"continuity/internal/planner"
	"continuity/internal/transform"
	"continuity/internal/validate"
)

const (
	framesDir   = "frames"
	baselineRel = "output/baseline.json"
)

// evaluateCache reads the container and sidecar metadata, updates the
// fingerprint store and returns the observed fingerprint, counters and the
// single decision reason for this run.
func evaluateCache(root string, frames []model.Frame) (cache.Fingerprint, cache.Counters, cache.Reason, error) {
	containerRel := filepath.Join(fixture.InputDir, fixture.ContainerName)
	containerPath, err := model.SafeJoin(root, containerRel)
	if err != nil {
		return cache.Fingerprint{}, cache.Counters{}, "", err
	}
	data, err := os.ReadFile(containerPath)
	if err != nil {
		return cache.Fingerprint{}, cache.Counters{}, "",
			model.WrapError(model.StageCache, 0, 0, "reading container", err)
	}
	containerVersion, _, err := fixture.ContainerHeader(data)
	if err != nil {
		return cache.Fingerprint{}, cache.Counters{}, "",
			model.WrapError(model.StageCache, 0, 0, "reading container version", err)
	}
	contentHash, err := fixture.ContentHash(data)
	if err != nil {
		return cache.Fingerprint{}, cache.Counters{}, "", err
	}
	meta, err := readMetadata(root)
	if err != nil {
		return cache.Fingerprint{}, cache.Counters{}, "", err
	}
	observed := cache.Fingerprint{
		Path:             containerRel,
		ContentSHA256:    contentHash,
		ContainerVersion: containerVersion,
		MetadataVersion:  meta.MetadataVersion,
	}
	store, err := cache.LoadStore(root)
	if err != nil {
		return cache.Fingerprint{}, cache.Counters{}, "", err
	}
	var counters cache.Counters
	var decision cache.Decision
	if store.Has(observed.Path) {
		// A pristine baseline was seeded before a mutation; compare against it
		// and preserve it so the difference stays reportable.
		decision = store.Compare(observed)
		switch decision.Reason {
		case cache.ReasonContentReplaced:
			counters.ContentInvalidations++
		case cache.ReasonContainerChanged, cache.ReasonMetadataChanged:
			counters.IndexInvalidations++
		}
		// After a content replacement the old baseline is stale; persist the
		// new fingerprint as the baseline for future runs.
		if decision.Reason == cache.ReasonContentReplaced {
			store.Evaluate(observed, &cache.Counters{})
			if err = store.Save(root); err != nil {
				return cache.Fingerprint{}, cache.Counters{}, "", err
			}
		}
	} else {
		// No prior baseline: this is a genuinely fresh first observation.
		decision = store.Evaluate(observed, &counters)
		if err = store.Save(root); err != nil {
			return cache.Fingerprint{}, cache.Counters{}, "", err
		}
	}
	return observed, counters, decision.Reason, nil
}

func readMetadata(root string) (fixture.Metadata, error) {
	path, err := model.SafeJoin(root, filepath.Join(fixture.InputDir, fixture.MetadataName))
	if err != nil {
		return fixture.Metadata{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fixture.Metadata{}, model.WrapError(model.StageCache, 0, 0, "reading metadata sidecar", err)
	}
	var meta fixture.Metadata
	if err = json.Unmarshal(raw, &meta); err != nil {
		return fixture.Metadata{}, model.WrapError(model.StageCache, 0, 0, "metadata sidecar is corrupt", err)
	}
	return meta, nil
}

// processRemaining processes every not-yet-validated frame, committing each
// frame artifact before its validated transition.
func processRemaining(cfg Config, jrnl *journal.Journal, frames []model.Frame, tp transform.Params) error {
	total := len(frames)
	resume := jrnl.ResumePoint(total)

	jobs := make([]pipeline.Job, 0, total)
	for i := range frames {
		if jrnl.StateOf(frames[i].Number) != journal.StateValidated && frames[i].Number >= resume {
			jobs = append(jobs, pipeline.Job{Frame: frames[i], Params: tp})
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	pool, err := pipeline.New(effectiveThreads(cfg.Threads, len(jobs)), jrnl)
	if err != nil {
		return err
	}
	outcomes, err := pool.Run(jobs)
	if err != nil {
		return err
	}
	for _, oc := range outcomes {
		if err = commitFrame(cfg.Workspace, oc); err != nil {
			return err
		}
		if err = jrnl.Transition(oc.Frame.Number, oc.Frame.Track, journal.StateValidated, ""); err != nil {
			return err
		}
		if cfg.InterruptAfter > 0 && int(oc.Frame.Number) == cfg.InterruptAfter {
			return ErrInterrupted
		}
	}
	return nil
}

// commitFrame lands one frame's payload and metadata. Both files are written
// before the validated transition, so a crash leaves only redoable,
// uncommitted temp output.
func commitFrame(root string, oc pipeline.Outcome) error {
	dir, err := model.SafeJoin(root, filepath.Join(validate.OutputDir, framesDir))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	num := int(oc.Frame.Number)
	binPath, err := model.SafeJoin(root, filepath.Join(validate.OutputDir, framesDir, frameFileName(num, "bin")))
	if err != nil {
		return err
	}
	if err = model.AtomicWrite(binPath, oc.Output.Payload, 0o644); err != nil {
		return err
	}
	doc, err := json.MarshalIndent(oc.Output, "", "  ")
	if err != nil {
		return err
	}
	jsonPath, err := model.SafeJoin(root, filepath.Join(validate.OutputDir, framesDir, frameFileName(num, "json")))
	if err != nil {
		return err
	}
	return model.AtomicWrite(jsonPath, doc, 0o644)
}

func frameFileName(num int, ext string) string {
	return frameStem(num) + "." + ext
}

func frameStem(num int) string {
	return "frame-" + fmt.Sprintf("%04d", num)
}

// effectiveThreads never starts more workers than there are jobs; extra
// configured threads would only sit idle.
func effectiveThreads(configured, jobCount int) int {
	if configured > jobCount {
		return jobCount
	}
	return configured
}

// commitAndVerify rebuilds the output purely from committed per-frame files,
// then runs the independent validator.
func commitAndVerify(root string, frames []model.Frame, sel *planner.Params) (*validate.Manifest, *validate.Report, error) {
	outputs := make([]model.OutputFrame, 0, len(frames))
	for i := range frames {
		num := int(frames[i].Number)
		jsonPath, err := model.SafeJoin(root, filepath.Join(validate.OutputDir, framesDir, frameStem(num)+".json"))
		if err != nil {
			return nil, nil, err
		}
		raw, err := os.ReadFile(jsonPath)
		if err != nil {
			return nil, nil, model.WrapError(model.StageValidate, frames[i].Number, 0, "reading committed frame metadata", err)
		}
		var of model.OutputFrame
		if err = json.Unmarshal(raw, &of); err != nil {
			return nil, nil, model.WrapError(model.StageValidate, frames[i].Number, 0, "committed frame metadata is corrupt", err)
		}
		binPath, err := model.SafeJoin(root, filepath.Join(validate.OutputDir, framesDir, frameStem(num)+".bin"))
		if err != nil {
			return nil, nil, err
		}
		payload, err := os.ReadFile(binPath)
		if err != nil {
			return nil, nil, model.WrapError(model.StageValidate, frames[i].Number, 0, "reading committed frame payload", err)
		}
		if model.Checksum(payload) != of.SHA256 {
			return nil, nil, model.NewError(model.StageValidate, frames[i].Number, 0, "committed payload hash mismatch")
		}
		of.Payload = payload
		outputs = append(outputs, of)
	}
	sort.SliceStable(outputs, func(i, k int) bool { return outputs[i].Number < outputs[k].Number })

	m, err := validate.WriteOutput(root, outputs, sel.SampleRate, sel.Channels, sel.TargetLUFS)
	if err != nil {
		return nil, nil, err
	}
	report, _, verr := validate.Verify(root, len(frames), sel.SampleRate, sel.Channels, sel.TargetLUFS)
	if verr != nil {
		return m, report, verr
	}
	return m, report, nil
}

type baselineDoc struct {
	ManifestSHA256 string   `json:"manifest_sha256"`
	FrameHashes    []string `json:"frame_hashes"`
}

// compareBaseline compares the completed run against the first completed run's
// hashes, leaving that baseline immutable so repeated runs report identical.
func compareBaseline(root string, m *validate.Manifest) (string, int, error) {
	path, err := model.SafeJoin(root, baselineRel)
	if err != nil {
		return "", 0, err
	}
	current := baselineDoc{ManifestSHA256: m.ManifestSHA256}
	for _, f := range m.Frames {
		current.FrameHashes = append(current.FrameHashes, f.SHA256)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		doc, merr := json.MarshalIndent(current, "", "  ")
		if merr != nil {
			return "", 0, merr
		}
		if err = model.AtomicWrite(path, doc, 0o644); err != nil {
			return "", 0, err
		}
		return "initial", 0, nil
	}
	if err != nil {
		return "", 0, model.WrapError(model.StageValidate, 0, 0, "reading baseline", err)
	}
	var base baselineDoc
	if err = json.Unmarshal(raw, &base); err != nil {
		return "", 0, model.WrapError(model.StageValidate, 0, 0, "baseline is corrupt", err)
	}
	changed := 0
	for i, h := range current.FrameHashes {
		if i >= len(base.FrameHashes) || base.FrameHashes[i] != h {
			changed++
		}
	}
	if changed == 0 && base.ManifestSHA256 == current.ManifestSHA256 {
		return "identical", 0, nil
	}
	return "changed", changed, nil
}

func summarize(jrnl *journal.Journal, counters cache.Counters,
	reason cache.Reason, resumeFrom model.FrameNumber, total int, runErr error) (*Summary, error) {
	counts := jrnl.CumulativeCounts()
	s := &Summary{
		Discovered:           counts[journal.StateDiscovered],
		Planned:              counts[journal.StatePlanned],
		Processed:            counts[journal.StateProcessed],
		Validated:            counts[journal.StateValidated],
		ResumeFrom:           resumeFrom,
		ManifestFrames:       total,
		ContentInvalidations: counters.ContentInvalidations,
		IndexInvalidations:   counters.IndexInvalidations,
		CacheReason:          reason,
	}
	return s, runErr
}

func summarizeFull(jrnl *journal.Journal, counters cache.Counters,
	reason cache.Reason, resumeFrom model.FrameNumber, m *validate.Manifest,
	comparison string, changed int) (*Summary, error) {
	s, _ := summarize(jrnl, counters, reason, resumeFrom, m.FrameCount, nil)
	s.ManifestSHA256 = m.ManifestSHA256
	s.OutputSHA256 = m.OutputSHA256
	s.Comparison = comparison
	s.ChangedHashes = changed
	return s, nil
}
