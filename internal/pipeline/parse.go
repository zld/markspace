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

// resumeState is the ingest prefix restored from a checkpoint.
type resumeState struct {
	Fragments []ingest.Fragment `json:"fragments"`
	ResumeAt  int64             `json:"resume_at"`
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
		if out.reused == 0 {
			// The index only describes the region from the start; a resumed
			// scan continues from an offset the index cannot address.
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
		if res.ResumeAt > out.resumeAt {
			out.resumeAt = res.ResumeAt
		}
		if len(out.fragments) == 0 && !res.Stopped {
			errs = append(errs, fault.New(fault.CodeMissingField, in.Name, scanFrom, "moof", "no fragments found"))
		}
	}
	return out, errs, nil
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
