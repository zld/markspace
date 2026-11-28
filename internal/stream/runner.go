package stream

import (
	"encoding/json"
	"os"
	"path/filepath"

	"avpipe/internal/config"
	"avpipe/internal/manifest"
	"avpipe/internal/model"
	"avpipe/internal/repair"
	"avpipe/internal/scan"
)

// RunRequest parameterizes one conversion run.
type RunRequest struct {
	InputDir       string
	StateDir       string
	MaxKbps        int
	BudgetMS       int
	TargetPermille int
	StopAfter      int // interrupt after this many new samples; -1 disables
	Resume         bool
}

// Defect is a reportable per-track problem carried into terminal output.
type Defect struct {
	Track       int    `json:"track"`
	Offset      int64  `json:"offset"`
	Code        string `json:"code"`
	Recoverable bool   `json:"recoverable"`
	Message     string `json:"message"`
}

// RunResult is everything the terminal layer prints after a run.
type RunResult struct {
	State            string
	CheckpointSample int
	Duplicate        int
	ManifestDiff     int
	ManifestValid    bool
	Manifest         *manifest.Manifest
	Config           *config.Config
	Defects          []Defect
	Locatable        bool
}

// frame is one de-duplicated, stable-keyed output record.
type frame struct {
	Key         string       `json:"key"`
	Track       int          `json:"track"`
	Seq         int          `json:"seq"`
	FixedTimeNS model.TimeNS `json:"fixed_time_ns"`
	InputOffset int64        `json:"input_offset"`
	Size        int          `json:"size"`
	Hash        string       `json:"hash"`
}

// Run performs or resumes a full conversion and returns its auditable result.
func Run(req RunRequest) (*RunResult, error) {
	idx, err := scan.Scan(req.InputDir)
	if err != nil {
		return nil, err
	}

	cpPath := filepath.Join(req.StateDir, StateFile)
	var cp *Checkpoint
	if req.Resume {
		cp, err = LoadCheckpoint(req.StateDir, idx.Digest)
		if err != nil {
			return nil, err
		}
	} else if _, statErr := os.Stat(cpPath); statErr == nil {
		// Fresh run over an existing state: rebuild from scratch.
		cp = NewCheckpoint(idx.Digest)
	} else {
		cp = NewCheckpoint(idx.Digest)
	}

	rep := repair.Repair(idx)
	cfg, cfgErr := config.Derive(rep, idx, req.MaxKbps, req.BudgetMS, req.TargetPermille)
	if cfgErr != nil {
		return nil, cfgErr
	}
	// indexed -> planned (a fresh checkpoint starts in indexed).
	if cp.State == StateIndexed {
		if err := cp.Move(StatePlanned); err != nil {
			return nil, err
		}
		if err := cp.Save(req.StateDir); err != nil {
			return nil, err
		}
	}
	if err := cp.Move(StateStreaming); err != nil {
		return nil, err
	}

	// One stable, globally ordered stream of repaired samples.
	ordered := orderSamples(rep)
	journalPath := filepath.Join(req.StateDir, JournalFile)
	jf, err := openJournal(journalPath, req.Resume)
	if err != nil {
		return nil, err
	}
	defer jf.Close()

	duplicate := 0
	start := cp.LastSample + 1
	for g := start; g < len(ordered); g++ {
		s := ordered[g]
		key := s.StableKey()
		f := frame{Key: key, Track: s.Track, Seq: s.Seq,
			FixedTimeNS: s.FixedTimeNS, InputOffset: s.InputOffset, Size: s.Size,
			Hash: scan.HashBytes(sampleContent(s))}
		if prev, ok := cp.WrittenKeys[key]; ok {
			if prev != f.Hash {
				duplicate++ // same stable key, different content: a real conflict
			}
			// An identical re-write is a no-op, never written twice.
		} else {
			line, err := json.Marshal(f)
			if err != nil {
				return nil, model.NewError(model.CodeIO, false, "marshal frame: %v", err)
			}
			if _, err := jf.Write(append(line, '\n')); err != nil {
				return nil, model.NewError(model.CodeIO, false, "journal: %v", err)
			}
			cp.WrittenKeys[key] = f.Hash
		}
		cp.LastSample = g
		// Checkpoint after each fully processed sample so the last complete
		// position survives an interruption; resume starts at g+1.
		if err := cp.Save(req.StateDir); err != nil {
			return nil, err
		}
		if req.StopAfter >= 0 && g >= req.StopAfter {
			cp.Interrupted = true
			cp.StopAfter = req.StopAfter
			if err := cp.Save(req.StateDir); err != nil {
				return nil, err
			}
			return &RunResult{
				State: StateStreaming, CheckpointSample: cp.LastSample,
				Duplicate: duplicate, Config: cfg, Defects: collectDefects(rep),
				Locatable: true,
			}, nil
		}
	}

	// All samples processed: commit.
	outputs, err := finalizeOutputs(req.StateDir, rep)
	if err != nil {
		return nil, err
	}
	m := manifest.Build(rep, cfg, idx, outputs)
	if err := m.Save(req.StateDir); err != nil {
		return nil, err
	}

	diff := 0
	if prior, err := manifest.Load(req.StateDir); err == nil {
		diff = manifest.Diff(prior, m)
	}
	if err := cp.Move(StateCommitted); err != nil {
		return nil, err
	}
	if err := cp.Save(req.StateDir); err != nil {
		return nil, err
	}
	problems := m.Check(rep, cfg, idx, req.StateDir)
	return &RunResult{
		State: StateCommitted, CheckpointSample: cp.LastSample,
		Duplicate: duplicate, ManifestDiff: diff, ManifestValid: len(problems) == 0,
		Manifest: m, Config: cfg, Defects: collectDefects(rep), Locatable: true,
	}, nil
}

// VerifyResult reports an explicit manifest verification.
type VerifyResult struct {
	Valid    bool
	Problems []string
	Manifest *manifest.Manifest
}

// Verify re-scans the input, re-derives repair/config and checks the stored
// manifest against freshly measured values and output hashes.
func Verify(inputDir, stateDir string, maxKbps, budgetMS, targetPermille int) (*VerifyResult, error) {
	idx, err := scan.Scan(inputDir)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Load(stateDir)
	if err != nil {
		return nil, err
	}
	if m.Digest != idx.Digest {
		return &VerifyResult{Valid: false, Manifest: m,
			Problems: []string{"input digest no longer matches manifest"}}, nil
	}
	rep := repair.Repair(idx)
	cfg, err := config.Derive(rep, idx, maxKbps, budgetMS, targetPermille)
	if err != nil {
		return nil, err
	}
	problems := m.Check(rep, cfg, idx, stateDir)
	return &VerifyResult{Valid: len(problems) == 0, Problems: problems, Manifest: m}, nil
}

// orderSamples flattens repaired tracks and applies the stable ordering rule.
func orderSamples(rep *repair.Report) []model.SampleMeta {
	var all []model.SampleMeta
	for i := range rep.Tracks {
		all = append(all, rep.Tracks[i].Samples...)
	}
	model.SortSamples(all)
	return all
}

func sampleContent(s model.SampleMeta) []byte {
	return []byte(s.StableKey() + ":" + jsonNumber(s.FixedTimeNS) + ":" +
		jsonNumber(s.InputOffset))
}

func openJournal(path string, resume bool) (*os.File, error) {
	flag := os.O_CREATE | os.O_WRONLY
	if resume {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return nil, model.NewError(model.CodeIO, false, "open journal: %v", err)
	}
	return f, nil
}

func collectDefects(rep *repair.Report) []Defect {
	var out []Defect
	for i := range rep.Tracks {
		d := rep.Tracks[i].Defect
		if d == nil {
			continue
		}
		out = append(out, Defect{
			Track: d.Track, Offset: d.Offset, Code: string(d.Code),
			Recoverable: d.Recoverable, Message: d.Message,
		})
	}
	return out
}
