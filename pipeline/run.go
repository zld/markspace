package pipeline

import (
	"fmt"

	"streamcheck/core"
)

// Stage is one pipeline step. Run receives the artifacts of all completed
// stages and returns its own JSON artifact.
type Stage struct {
	Name string
	Run  func(artifacts map[string]string) (string, error)
}

// Engine executes an ordered stage list, checkpointing after each stage.
type Engine struct {
	Work   string
	Inputs []string // input files relative to Work, covered by the digest
	Stages []Stage
}

// StageIndex returns the position of a stage name, or -1.
func (e *Engine) StageIndex(name string) int {
	for i, s := range e.Stages {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// InputDigest computes the current digest of the engine's inputs.
func (e *Engine) InputDigest() (string, error) {
	return DigestInputs(e.Work, e.Inputs)
}

// Execute runs stages from position start, saving a checkpoint after
// each. If a stage name equals stopAfter, the checkpoint is saved and
// Execute returns core.ErrStop — an intentional interrupt, exit code 75.
func (e *Engine) Execute(start int, artifacts map[string]string, inputDigest, stopAfter string) (map[string]string, error) {
	if artifacts == nil {
		artifacts = map[string]string{}
	}
	if start < 0 || start > len(e.Stages) {
		return artifacts, core.E(core.KindUsage, "execute", fmt.Sprintf("bad start stage %d", start), nil)
	}
	for i := start; i < len(e.Stages); i++ {
		st := e.Stages[i]
		out, err := st.Run(artifacts)
		if err != nil {
			return artifacts, err
		}
		artifacts[st.Name] = out
		cp := Checkpoint{
			InputDigest:  inputDigest,
			Stage:        st.Name,
			ResultDigest: DigestArtifacts(artifacts),
			Artifacts:    artifacts,
		}
		if err := SaveCheckpoint(e.Work, cp); err != nil {
			return artifacts, err
		}
		if st.Name == stopAfter {
			return artifacts, core.ErrStop
		}
	}
	return artifacts, nil
}

// ResumePoint validates a loaded checkpoint against the current inputs
// and returns the stage index to continue from.
func (e *Engine) ResumePoint(cp *Checkpoint) (int, error) {
	digest, err := e.InputDigest()
	if err != nil {
		return -1, err
	}
	if digest != cp.InputDigest {
		return -1, core.E(core.KindCheckpoint, "resume",
			"inputs changed since checkpoint; refusing to resume", nil)
	}
	idx := e.StageIndex(cp.Stage)
	if idx < 0 {
		return -1, core.E(core.KindCheckpoint, "resume",
			"checkpoint names unknown stage "+cp.Stage, nil)
	}
	return idx + 1, nil
}
