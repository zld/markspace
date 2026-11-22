package pipeline

import (
	"encoding/json"
	"errors"

	"mediapack/merr"
)

// Stage is one processing phase. Run receives the JSON payloads of all
// earlier stages and returns a JSON-marshallable result.
type Stage struct {
	Name string
	Run  func(deps map[string]json.RawMessage) (any, error)
}

// Runner executes stages in order, reusing valid checkpoints and
// re-executing from the first invalid or missing one.
type Runner struct {
	Store        *Store
	InputDigest  string // digest over all input assets
	ParamsDigest string // digest over all run parameters
}

// Outcome summarizes one pipeline run.
type Outcome struct {
	ResumeFrom string // first stage actually executed ("none" if all reused)
	Results    map[string]json.RawMessage
	Provenance *Provenance
}

// Decode unmarshals an earlier stage's payload for a dependent stage.
func Decode(results map[string]json.RawMessage, stage string, v any) error {
	raw, ok := results[stage]
	if !ok {
		return merr.Resumef("pipeline", "stage %s has no payload", stage)
	}
	return json.Unmarshal(raw, v)
}

// Run executes the stages. A checkpoint is reused only when its digest
// verifies and it matches the current input digest, parameter digest and
// predecessor chain; otherwise it and every later checkpoint are
// discarded and the stage is re-executed. A corrupt checkpoint therefore
// falls back to the last verified stage and never reuses later artifacts.
func (r *Runner) Run(stages []Stage) (*Outcome, error) {
	out := &Outcome{Results: map[string]json.RawMessage{}}
	prev := ""
	resumeFrom := ""
	var records []StageRecord
	names := make([]string, len(stages))
	for i, st := range stages {
		names[i] = st.Name
	}
	for i, st := range stages {
		cp, err := r.Store.Load(st.Name)
		if err == nil && cp.InputDigest == r.InputDigest && cp.ParamsDigest == r.ParamsDigest && cp.PrevDigest == prev {
			// valid checkpoint: reuse
			out.Results[st.Name] = cp.Payload
			records = append(records, StageRecord{Name: st.Name, ResultDigest: cp.ResultDigest, Reused: true})
			prev = cp.Digest
			continue
		}
		if err == nil || !errors.Is(err, ErrNotFound) {
			// stale (digest/params/chain mismatch) or corrupt checkpoint:
			// discard it and everything downstream
			if rmErr := r.Store.Remove(names[i:]...); rmErr != nil {
				return nil, rmErr
			}
		}
		if resumeFrom == "" {
			resumeFrom = st.Name
		}
		result, err := st.Run(out.Results)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, merr.Wrap(merr.KindInternal, "pipeline", "", "marshal stage "+st.Name, err)
		}
		ncp := &Checkpoint{
			Stage:        st.Name,
			InputDigest:  r.InputDigest,
			ParamsDigest: r.ParamsDigest,
			PrevDigest:   prev,
			Payload:      raw,
			ResultDigest: ResultDigestOf(raw),
		}
		ncp.Digest = ncp.ComputeDigest()
		if err := r.Store.Save(ncp); err != nil {
			return nil, err
		}
		out.Results[st.Name] = raw
		records = append(records, StageRecord{Name: st.Name, ResultDigest: ncp.ResultDigest})
		prev = ncp.Digest
	}
	if resumeFrom == "" {
		resumeFrom = "none"
	}
	out.ResumeFrom = resumeFrom
	out.Provenance = NewProvenance(r.InputDigest, r.ParamsDigest, records)
	return out, nil
}
