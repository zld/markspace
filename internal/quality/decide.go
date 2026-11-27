package quality

import (
	"math"

	"mediacore/internal/fault"
)

// Decision is the full selection result.
type Decision struct {
	Config   Config     `json:"config"`
	Verdicts []Verdict  `json:"verdicts"`
	Selected *Candidate `json:"selected,omitempty"`
	Reason   string     `json:"reason"`
	Fallback bool       `json:"fallback"`
}

// Stable decision reasons.
const (
	ReasonSelected          = "selected: quality, budget and rate satisfied, smallest output"
	ReasonSelectedNoRate    = "selected: quality and budget satisfied, smallest output; rate outside tolerance is reported"
	ReasonQualityFailed     = "rejected: objective quality below floor"
	ReasonBudgetExceeded    = "rejected: processing budget exceeded"
	ReasonNoCandidate       = "failed: no candidate met the quality floor"
	ReasonNoBudgetCandidate = "failed: quality floor met but every candidate exceeded the budget"
	ReasonEmpty             = "failed: no candidates supplied"
)

// Decide applies the documented priority. The quality gate is absolute and is
// never lowered: if nothing passes it, no candidate is selected.
func Decide(cfg Config, candidates []Candidate) (*Decision, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	d := &Decision{Config: cfg, Verdicts: make([]Verdict, 0, len(candidates))}
	if len(candidates) == 0 {
		d.Reason = ReasonEmpty
		return d, fault.Newf(fault.CodeNoCandidate, "", "no candidates supplied")
	}
	var qualityPass []int
	for i, c := range candidates {
		v := Verdict{Candidate: c}
		v.QualityOK = c.PSNRdB >= cfg.QualityMin
		v.BudgetOK = c.ProcessMS <= cfg.Budget
		v.RateOK = math.Abs(c.Deviation(cfg.TargetKbps)) <= cfg.RateTolerance
		switch {
		case !v.QualityOK:
			v.Reason = ReasonQualityFailed
		case !v.BudgetOK:
			v.Reason = ReasonBudgetExceeded
		}
		d.Verdicts = append(d.Verdicts, v)
		if v.QualityOK {
			qualityPass = append(qualityPass, i)
		}
	}
	if len(qualityPass) == 0 {
		d.Reason = ReasonNoCandidate
		d.Fallback = true
		return d, fault.Newf(fault.CodeNoCandidate, "",
			"no candidate reached the %.2f dB quality floor; floor is not lowered", cfg.QualityMin)
	}
	best := -1
	for _, i := range qualityPass {
		v := &d.Verdicts[i]
		if !v.BudgetOK {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		b := &d.Verdicts[best]
		if v.Candidate.SizeBytes < b.Candidate.SizeBytes ||
			(v.Candidate.SizeBytes == b.Candidate.SizeBytes && v.Candidate.Name < b.Candidate.Name) {
			best = i
		}
	}
	if best < 0 {
		d.Reason = ReasonNoBudgetCandidate
		d.Fallback = true
		return d, fault.Newf(fault.CodeBudgetExceeded, "",
			"every candidate over the %.2f dB floor exceeded the %d ms budget", cfg.QualityMin, cfg.Budget)
	}
	d.Verdicts[best].Selected = true
	sel := d.Verdicts[best].Candidate
	d.Selected = &sel
	if d.Verdicts[best].RateOK {
		d.Reason = ReasonSelected
	} else {
		d.Reason = ReasonSelectedNoRate
	}
	return d, nil
}
