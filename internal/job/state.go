// Package job persists a phase-based pipeline state machine to local files
// so interrupted runs resume from the last complete checkpoint and reruns
// produce identical artifact hashes.
package job

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediad/internal/clock"
)

// Phase is a named, ordered pipeline stage.
type Phase string

const (
	PhaseParse Phase = "parse"
	PhaseMap   Phase = "map"
	PhaseIndex Phase = "index"
	PhaseMerge Phase = "merge"
)

// State is the persisted job state. It contains no credentials, only phase
// completion markers and output hashes.
type State struct {
	JobID     string           `json:"job_id"`
	Completed []Phase          `json:"completed"`
	Hashes    map[Phase]string `json:"hashes"`
	Done      bool             `json:"done"`
	Failure   string           `json:"failure,omitempty"`
}

// NewState returns a fresh state for a job.
func NewState(jobID string) *State {
	return &State{JobID: jobID, Hashes: make(map[Phase]string)}
}

// Has reports whether a phase completed (checkpoint is trustworthy).
func (s *State) Has(p Phase) bool {
	for _, c := range s.Completed {
		if c == p {
			return true
		}
	}
	return false
}

// markComplete records a phase output hash.
func (s *State) markComplete(p Phase, output []byte) {
	if !s.Has(p) {
		s.Completed = append(s.Completed, p)
	}
	sum := sha256.Sum256(output)
	s.Hashes[p] = hex.EncodeToString(sum[:])
}

// Digest is the deterministic job-level hash over ordered phase hashes.
func (s *State) Digest() string {
	phases := make([]string, 0, len(s.Hashes))
	for p := range s.Hashes {
		phases = append(phases, string(p))
	}
	sort.Strings(phases)
	h := sha256.New()
	for _, p := range phases {
		h.Write([]byte(p))
		h.Write([]byte(s.Hashes[Phase(p)]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PhaseFunc computes a phase output from the previous phase output.
type PhaseFunc func(prev []byte) ([]byte, error)

// Result summarizes one Run invocation.
type Result struct {
	ResumedFromCheckpoint bool
	Digest                string
}

// Run executes phases in order, skipping phases whose checkpoints already
// exist and are recorded complete. On a phase error the failure reason is
// persisted before returning.
func Run(store *Store, jobID string, phases []Phase, fns map[Phase]PhaseFunc) (Result, error) {
	st, err := store.LoadState(jobID)
	if err != nil {
		return Result{}, err
	}
	res := Result{}
	var prev []byte
	for _, ph := range phases {
		fn, ok := fns[ph]
		if !ok {
			return res, clock.New(clock.ErrCheckpoint, "no function for phase %s", ph)
		}
		if st.Has(ph) {
			data, err := store.ReadCheckpoint(ph)
			if err != nil {
				return res, err
			}
			// A recorded-complete phase whose checkpoint is missing or
			// corrupt must not be trusted.
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != st.Hashes[ph] {
				return res, clock.New(clock.ErrCheckpoint,
					"checkpoint %s hash mismatch", ph)
			}
			prev = data
			res.ResumedFromCheckpoint = true
			continue
		}
		out, err := fn(prev)
		if err != nil {
			st.Failure = err.Error()
			_ = store.SaveState(st) // best-effort failure record
			return res, err
		}
		if err := store.WriteCheckpoint(ph, out); err != nil {
			return res, err
		}
		st.markComplete(ph, out)
		if err := store.SaveState(st); err != nil {
			return res, err
		}
		prev = out
	}
	st.Done = true
	st.Failure = ""
	if err := store.SaveState(st); err != nil {
		return res, err
	}
	res.Digest = st.Digest()
	return res, nil
}
