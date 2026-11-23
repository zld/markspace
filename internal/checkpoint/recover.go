package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Decision describes whether a previous run can be reused.
type Decision struct {
	Resume    bool                       `json:"resume"`
	LastStage Stage                      `json:"last_stage"`
	Completed []Stage                    `json:"completed"`
	Reason    string                     `json:"reason,omitempty"`
	ResumeAt  int64                      `json:"resume_at"`
	Payload   map[string]json.RawMessage `json:"-"`
	SavedAt   string                     `json:"saved_at,omitempty"`
}

// Expect is what a resuming run requires from a checkpoint.
type Expect struct {
	InputDigest  string
	ConfigDigest string
	// VerifiedOutput is the path of previously written output that must not
	// be overwritten if verification of the checkpoint state fails.
	VerifiedOutput string
}

// Recover validates a checkpoint against the current inputs and configuration.
// It refuses to resume on any mismatch and reports which stage can restart.
func Recover(store *Store, want Expect) (Decision, error) {
	st, err := store.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Decision{Resume: false, Reason: "no checkpoint"}, nil
		}
		return Decision{Resume: false, Reason: err.Error()}, err
	}
	d := Decision{
		LastStage: st.LastStage,
		Completed: st.Completed,
		ResumeAt:  st.ResumeAt,
		Payload:   st.Payload,
		SavedAt:   st.SavedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if want.InputDigest != "" && st.InputDigest != want.InputDigest {
		d.Reason = fmt.Sprintf("input digest %s != checkpoint %s", short(want.InputDigest), short(st.InputDigest))
		return d, mismatch(want, d.Reason)
	}
	if want.ConfigDigest != "" && st.ConfigDigest != want.ConfigDigest {
		d.Reason = fmt.Sprintf("config digest %s != checkpoint %s", short(want.ConfigDigest), short(st.ConfigDigest))
		return d, mismatch(want, d.Reason)
	}
	if Rank(st.LastStage) < 0 {
		d.Reason = "checkpoint has no complete stage"
		return d, mismatch(want, d.Reason)
	}
	d.Resume = true
	d.Reason = "checkpoint matches inputs and configuration"
	return d, nil
}

func mismatch(want Expect, reason string) error {
	// A previously verified output is left untouched on refusal: this function
	// never deletes or rewrites it.
	if want.VerifiedOutput != "" {
		if _, err := os.Stat(want.VerifiedOutput); err == nil {
			reason += fmt.Sprintf("; verified output %s left intact", filepath.Base(want.VerifiedOutput))
		}
	}
	return fmt.Errorf("%w: %s", ErrMismatch, reason)
}

func short(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}

// NextStage returns the stage after last, or "" when the run is complete.
func NextStage(last Stage) (Stage, bool) {
	r := Rank(last)
	if r < 0 || r+1 >= len(stageOrder) {
		return "", false
	}
	return stageOrder[r+1], true
}

// Complete reports whether the stage is already recorded as done.
func Complete(st State, stage Stage) bool {
	for _, s := range st.Completed {
		if s == stage {
			return true
		}
	}
	return false
}

// Advance returns a copy of st with stage recorded and LastStage updated.
func Advance(st State, stage Stage, resumeAt int64) State {
	if !Complete(st, stage) {
		st.Completed = append(st.Completed, stage)
	}
	if Rank(stage) > Rank(st.LastStage) {
		st.LastStage = stage
	}
	st.ResumeAt = resumeAt
	return st
}
