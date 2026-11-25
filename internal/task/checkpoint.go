package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Checkpoint struct {
	TaskKey string          `json:"task_key"`
	Stage   Stage           `json:"stage"`
	Payload json.RawMessage `json:"payload"`
	SHA256  string          `json:"sha256"`
}

type ResumePlan struct {
	LastValid      *Checkpoint `json:"last_valid,omitempty"`
	CompletedStage Stage       `json:"completed_stage"`
	ResumeStage    Stage       `json:"resume_stage"`
	CorruptStages  []Stage     `json:"corrupt_stages,omitempty"`
}

func checkpointDigest(key string, stage Stage, payload []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", key, stage)
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func checkpointPath(dir, key string, stage Stage) string {
	return filepath.Join(dir, key+"."+string(stage)+".ckpt")
}

func (s *Store) SaveCheckpoint(taskKey string, stage Stage, payload any) (*Checkpoint, error) {
	return SaveCheckpoint(s.dir, taskKey, stage, payload)
}

// SaveCheckpoint writes and atomically renames a verified stage checkpoint.
func SaveCheckpoint(dir, taskKey string, stage Stage, payload any) (*Checkpoint, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cp := Checkpoint{TaskKey: taskKey, Stage: stage, Payload: raw}
	cp.SHA256 = checkpointDigest(taskKey, stage, raw)
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return nil, err
	}
	path := checkpointPath(dir, taskKey, stage)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return &cp, nil
}

func verifyCheckpoint(b []byte, taskKey string) (*Checkpoint, error) {
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, err
	}
	if cp.TaskKey != taskKey {
		return nil, fmt.Errorf("checkpoint task key %q != %q", cp.TaskKey, taskKey)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, cp.Payload); err != nil {
		return nil, fmt.Errorf("compact checkpoint payload: %w", err)
	}
	cp.Payload = compact.Bytes()
	if cp.SHA256 != checkpointDigest(cp.TaskKey, cp.Stage, cp.Payload) {
		return nil, fmt.Errorf("checksum mismatch")
	}
	validStage := false
	for _, st := range Stages {
		if cp.Stage == st {
			validStage = true
		}
	}
	if !validStage {
		return nil, fmt.Errorf("unknown stage %q", cp.Stage)
	}
	return &cp, nil
}

// LoadResumePlan scans from the final stage backwards. A corrupt checkpoint is
// quarantined and the previous valid checkpoint becomes the resume point.
func LoadResumePlan(dir, taskKey string) (ResumePlan, error) {
	plan := ResumePlan{ResumeStage: StageFingerprint}
	var ioErrs []error
	for i := len(Stages) - 1; i >= 0; i-- {
		stage := Stages[i]
		path := checkpointPath(dir, taskKey, stage)
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			ioErrs = append(ioErrs, err)
			continue
		}
		cp, verr := verifyCheckpoint(b, taskKey)
		if verr != nil {
			plan.CorruptStages = append(plan.CorruptStages, stage)
			_ = os.Rename(path, path+".corrupt")
			continue
		}
		plan.LastValid = cp
		plan.CompletedStage = cp.Stage
		if cp.Stage == StageDone {
			plan.ResumeStage = StageDone
		} else {
			plan.ResumeStage = NextStage(cp.Stage)
		}
		break
	}
	return plan, errorsJoin(ioErrs)
}

func (s *Store) ResumePlan(taskKey string) (ResumePlan, error) {
	return LoadResumePlan(s.dir, taskKey)
}

func errorsJoin(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("checkpoint IO errors: %v", errs)
}
