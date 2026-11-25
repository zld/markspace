// Package pipeline coordinates the resumable stages without containing CLI
// flag or terminal formatting concerns.
package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
	"archivecheck/internal/plan"
	"archivecheck/internal/recovery"
	"archivecheck/internal/store"
)

var errInterrupt = errors.New("simulated stage interruption")

// Config configures one acceptance run.
type Config struct {
	InputPath   string
	OutputDir   string
	Bitrates    []int64
	ResumeAfter int
	Restarted   bool
	Log         io.Writer
}

// Result contains canonical output and repeat-comparison facts.
type Result struct {
	Manifest                 model.OutputManifest
	ManifestBytes            []byte
	ManifestSHA256           string
	ManifestPath             string
	JournalPath              string
	Resume                   recovery.Resume
	PreviousManifestSHA256   string
	RepeatManifestByteDiff   int
	RepeatArtifactDigestDiff int
	SimulatedInterruption    bool
}

// Run executes the pipeline, transparently restarting once after the requested
// number of committed stage transitions. The restart loads state from disk.
func Run(cfg Config) (*Result, error) {
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	res, err := execute(cfg)
	if errors.Is(err, errInterrupt) && cfg.ResumeAfter > 0 {
		fmt.Fprintf(cfg.Log, "simulated_interruption=after_%d_committed_transitions\n", cfg.ResumeAfter)
		cfg.ResumeAfter = 0
		cfg.Restarted = true
		res, err = execute(cfg)
		if res != nil {
			res.SimulatedInterruption = true
		}
		return res, err
	}
	return res, err
}

type processor struct {
	cfg          Config
	input        model.InputManifest
	inputSHA     string
	recordID     string
	journalPath  string
	manifestPath string
	rootPath     string
	j            *store.Journal
	resume       recovery.Resume
}

func execute(cfg Config) (*Result, error) {
	if cfg.InputPath == "" {
		return nil, apperrors.E(apperrors.KindInvalidArgument, "missing -input")
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "./tmp-out"
	}
	input, inputSHA, err := loadInput(cfg.InputPath)
	if err != nil {
		return nil, err
	}
	bitrates := append([]int64(nil), cfg.Bitrates...)
	if len(bitrates) == 0 {
		bitrates = append([]int64(nil), input.Bitrates...)
	}
	if len(bitrates) == 0 {
		return nil, apperrors.E(apperrors.KindInvalidArgument, "no target bitrates supplied")
	}
	slices.Sort(bitrates)
	params := model.Parameters{Bitrates: bitrates}
	paramBytes, err := model.CanonicalJSON(params)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindManifest, err)
	}
	recordID := model.SHA256Bytes([]byte(inputSHA + model.StageVersion + string(paramBytes)))

	inputDir := filepath.Dir(cfg.InputPath)
	for i := range input.Assets {
		if input.Assets[i].Path != "" && !filepath.IsAbs(input.Assets[i].Path) {
			input.Assets[i].Path = filepath.Join(inputDir, input.Assets[i].Path)
		}
	}

	journalPath := filepath.Join(cfg.OutputDir, "state", recordID, store.JournalName)
	manifestPath := filepath.Join(cfg.OutputDir, "state", recordID, "output-manifest.json")
	rootPath := filepath.Join(cfg.OutputDir, "manifest.json")
	j, found, err := store.Load(journalPath)
	if err != nil {
		return nil, err
	}
	if !found {
		j = store.NewJournal(recordID, inputSHA, params)
		if err := j.SaveAtomic(journalPath); err != nil {
			return nil, err
		}
	}
	j.Parameters = params

	p := &processor{
		cfg: cfg, input: input, inputSHA: inputSHA, recordID: recordID,
		journalPath: journalPath, manifestPath: manifestPath, rootPath: rootPath,
		j: j, resume: recovery.FromJournal(j),
	}
	fmt.Fprintf(cfg.Log,
		"journal_recovery=%t journal_path=%s start_stage=%s restarted_from_disk=%t from_scratch=%t\n",
		found, journalPath, p.resume.StartStage, cfg.Restarted, !found)

	if j.Stage == plan.StageFailed {
		if _, err := recovery.RetryFailed(j); err != nil {
			return nil, err
		}
		if err := j.SaveAtomic(journalPath); err != nil {
			return nil, err
		}
	}
	if j.Stage == plan.StageCompleted {
		return p.completed()
	}
	return p.process()
}

func loadInput(path string) (model.InputManifest, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return model.InputManifest{}, "", apperrors.Wrap(apperrors.KindManifest, err).WithPath(path).WithStage("load_input")
	}
	var m model.InputManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return model.InputManifest{}, "", apperrors.Wrap(apperrors.KindManifest, err).WithPath(path).WithStage("load_input")
	}
	sha, _, err := model.HashCanonical(m)
	if err != nil {
		return m, "", apperrors.Wrap(apperrors.KindManifest, err)
	}
	return m, sha, nil
}

func (p *processor) advance(to plan.Stage, reason string) error {
	if err := p.j.Transition(to, reason); err != nil {
		return err
	}
	if err := p.j.SaveAtomic(p.journalPath); err != nil {
		return err
	}
	if p.cfg.ResumeAfter > 0 && len(p.j.Transitions) >= p.cfg.ResumeAfter {
		return errInterrupt
	}
	return nil
}

func (p *processor) fail(err error) error {
	if p.j.Stage != plan.StageFailed && p.j.Stage != plan.StageCompleted {
		if ferr := p.j.Fail(err); ferr == nil {
			_ = p.j.SaveAtomic(p.journalPath)
		}
	}
	return err
}
