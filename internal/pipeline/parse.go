package pipeline

import (
	"fmt"

	"mediacore/internal/fault"
	"mediacore/internal/ingest"
)

// parsedInputs is the ingest-stage output.
type parsedInputs struct {
	inits     []*ingest.InitInfo
	fragments []ingest.Fragment
	errors    []*fault.Error
	stopped   bool
	resumeAt  int64
	// reused is the number of fragments restored from a checkpoint instead of
	// being parsed again.
	reused int
}

// resumeState is the ingest prefix restored from a checkpoint, including the
// diagnostics already observed, so a resumed run reports exactly the same
// faults as a fresh one.
type resumeState struct {
	Fragments []ingest.Fragment `json:"fragments"`
	ResumeAt  int64             `json:"resume_at"`
	Errors    []*fault.Error    `json:"errors,omitempty"`
	Stopped   bool              `json:"stopped,omitempty"`
}

// parseInputs walks every input file: the initialization segment is validated
// first, then the index is read if present and the fragment region is scanned.
// When resume is non-nil, fragments already recorded in the checkpoint are
// restored and scanning starts at the recorded byte offset, so completed work
// is never repeated.
func parseInputs(inputs []Input, lim ingest.Limits, resume *resumeState) (parsedInputs, []*fault.Error, error) {
	var (
		out  parsedInputs
		errs []*fault.Error
	)
	for _, in := range inputs {
		r, f := ingest.NewReader(in.Name, in.Data, lim)
		if f != nil {
			errs = append(errs, f)
			continue
		}
		info, f := ingest.ParseInit(r)
		if f != nil {
			errs = append(errs, f)
			return out, errs, nil
		}
		out.inits = append(out.inits, info)
		regionStart := info.MoovOffset + info.MoovSize
		scanFrom := regionStart
		if resume != nil && resume.ResumeAt > scanFrom && resume.ResumeAt <= r.Len() {
			out.fragments = append(out.fragments, resume.Fragments...)
			out.reused = len(resume.Fragments)
			scanFrom = resume.ResumeAt
		}
		var idx *ingest.Index
		if out.reused > 0 {
			// Resuming: the index diagnostics are replayed from the checkpoint
			// rather than re-derived, and the tail is scanned without index
			// coverage so the already-recorded gap is not reported twice.
			out.stopped = resume.Stopped
		} else {
			var idxFault *fault.Error
			idx, idxFault = ingest.ScanIndex(r, regionStart)
			if idxFault != nil {
				errs = append(errs, idxFault)
			}
		}
		res := r.ScanFragmentsBounded(scanFrom, idx)
		for _, frag := range res.Fragments {
			frag.Index = len(out.fragments)
			out.fragments = append(out.fragments, frag)
		}
		errs = append(errs, res.Errors...)
		if res.Stopped {
			out.stopped = true
		}
		if resume != nil && out.reused > 0 {
			// Replay the prefix diagnostics, then drop the exact duplicates
			// the tail scan re-observes at the resume point.
			replayed := append(append([]*fault.Error(nil), resume.Errors...), errs...)
			errs = dedupe(replayed)
			resume = nil
		}
		if res.ResumeAt > out.resumeAt {
			out.resumeAt = res.ResumeAt
		}
		if len(out.fragments) == 0 && !res.Stopped {
			errs = append(errs, fault.New(fault.CodeMissingField, in.Name, scanFrom, "moof", "no fragments found"))
		}
	}
	return out, errs, nil
}

// dedupe removes repeated (code, offset, fragment) diagnostics while
// preserving first-seen order.
func dedupe(errs []*fault.Error) []*fault.Error {
	type key struct {
		code   fault.Code
		offset int64
		seq    uint32
		hasSeq bool
	}
	seen := make(map[key]bool, len(errs))
	out := errs[:0]
	for _, e := range errs {
		k := key{e.Code, e.Offset, e.FragmentSeq, e.HasSeq}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// Validate rejects configurations that cannot produce meaningful output.
func (c Config) Validate() error {
	if c.Fragments <= 0 {
		return fmt.Errorf("fragments must be positive")
	}
	if c.Tracks <= 0 {
		return fmt.Errorf("tracks must be positive")
	}
	if c.Timescale == 0 {
		return fmt.Errorf("timescale must be positive")
	}
	if c.FrameWidth < 0 || c.FrameHeight < 0 {
		return fmt.Errorf("frame dimensions must not be negative")
	}
	return nil
}
