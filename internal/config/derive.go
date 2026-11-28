// Package config derives a quantified conversion configuration from the
// repaired timeline. Every chosen value is backed by an explicit measurement
// and a printed reason; unsatisfiable constraints are reported, not guessed.
package config

import (
	"strings"

	"avpipe/internal/model"
	"avpipe/internal/repair"
	"avpipe/internal/scan"
)

// ProcMSPerSample is the documented, deterministic per-sample processing cost
// used to turn sample counts into a processing-time estimate.
const ProcMSPerSample = 1

// PermilleBase is 100% expressed in permille.
const PermilleBase = 1000

// Decision records one configuration choice with its full evidence chain.
type Decision struct {
	Item       string `json:"item"`
	Measured   string `json:"measured"`
	Constraint string `json:"constraint"`
	Chosen     string `json:"chosen"`
	Reason     string `json:"reason"`
}

// Config is the derived, evidence-backed conversion configuration.
type Config struct {
	Feasible         bool       `json:"feasible"`
	Reasons          []string   `json:"reasons,omitempty"`
	Kbps             int        `json:"kbps"`
	BudgetMS         int        `json:"budget_ms"`
	CoveragePermille int        `json:"coverage_permille"`
	DurationMS       int        `json:"duration_ms"`
	MeasuredKbps     int        `json:"measured_kbps"`
	EstProcMS        int        `json:"est_proc_ms"`
	KeptSamples      int        `json:"kept_samples"`
	TotalSamples     int        `json:"total_samples"`
	Decisions        []Decision `json:"decisions"`
}

// Explain renders the per-item measurement/constraint/reason lines.
func (c *Config) Explain() string {
	var b strings.Builder
	for _, d := range c.Decisions {
		b.WriteString("config: ")
		b.WriteString(d.Item)
		b.WriteString(" measured=")
		b.WriteString(d.Measured)
		b.WriteString(" constraint=")
		b.WriteString(d.Constraint)
		b.WriteString(" chosen=")
		b.WriteString(d.Chosen)
		b.WriteString(" reason=")
		b.WriteString(d.Reason)
		b.WriteString("\n")
	}
	return b.String()
}

func pct(permille int) string {
	whole, tenths := permille/10, permille%10
	return itoa(whole) + "." + itoa(tenths) + "%"
}

// Derive computes the configuration from measured timeline properties.
func Derive(rep *repair.Report, idx *scan.Index,
	maxKbps, budgetMS, targetPermille int) (*Config, error) {

	var (
		declared, kept int
		bytes          int64
		minNS          model.TimeNS = 1 << 62
		maxEndNS       model.TimeNS
	)
	for _, td := range idx.Desc.Tracks {
		declared += td.Samples
		stepNS := model.TicksToNS(td.StepTicks, td.ClockRate)
		for _, tr := range rep.Tracks {
			if tr.Track != td.ID || len(tr.Samples) == 0 {
				continue
			}
			kept += len(tr.Samples)
			if tr.Samples[0].FixedTimeNS < minNS {
				minNS = tr.Samples[0].FixedTimeNS
			}
			end := tr.Samples[len(tr.Samples)-1].FixedTimeNS + stepNS
			if end > maxEndNS {
				maxEndNS = end
			}
			for _, s := range tr.Samples {
				bytes += int64(s.Size)
			}
		}
	}
	if declared == 0 {
		return nil, model.NewError(model.CodeInfeasible, false, "no samples declared")
	}
	if minNS == 1<<62 {
		minNS = 0
	}
	durationNS := maxEndNS - minNS
	durationMS := int((durationNS + 1_000_000 - 1) / 1_000_000)
	if durationMS < 1 {
		durationMS = 1
	}
	coverage := kept * PermilleBase / declared
	// Measured average bitrate, rounded up to a whole kbps.
	measuredKbps := int((bytes*8 + int64(durationMS)*1000 - 1) /
		(int64(durationMS) * 1000))
	estProcMS := kept * ProcMSPerSample

	c := &Config{
		Kbps: measuredKbps, BudgetMS: estProcMS, CoveragePermille: coverage,
		DurationMS: durationMS, MeasuredKbps: measuredKbps, EstProcMS: estProcMS,
		KeptSamples: kept, TotalSamples: declared,
	}

	// 1) coverage
	if coverage >= targetPermille {
		c.Decisions = append(c.Decisions, Decision{
			Item: "coverage", Measured: pct(coverage),
			Constraint: ">=" + pct(targetPermille), Chosen: pct(coverage),
			Reason: "repaired samples meet target coverage",
		})
	} else {
		c.Reasons = append(c.Reasons, "coverage "+pct(coverage)+
			" below target "+pct(targetPermille)+" (a stopped/truncated track)")
	}

	// 2) bitrate budget
	if measuredKbps <= maxKbps {
		c.Decisions = append(c.Decisions, Decision{
			Item: "bitrate", Measured: itoa(measuredKbps) + "kbps",
			Constraint: "<=" + itoa(maxKbps) + "kbps",
			Chosen:     itoa(measuredKbps) + "kbps",
			Reason:     "measured average bitrate fits the cap",
		})
	} else {
		c.Reasons = append(c.Reasons, "required bitrate "+itoa(measuredKbps)+
			"kbps exceeds cap "+itoa(maxKbps)+"kbps")
	}

	// 3) processing deadline
	if estProcMS <= budgetMS {
		c.Decisions = append(c.Decisions, Decision{
			Item: "processing", Measured: itoa(estProcMS) + "ms",
			Constraint: "<=" + itoa(budgetMS) + "ms",
			Chosen:     itoa(estProcMS) + "ms",
			Reason: itoa(kept) + " samples at " + itoa(ProcMSPerSample) +
				"ms each fit the deadline",
		})
	} else {
		c.Reasons = append(c.Reasons, "estimated processing "+itoa(estProcMS)+
			"ms exceeds deadline "+itoa(budgetMS)+"ms")
	}

	if len(c.Reasons) > 0 {
		c.Feasible = false
		return c, model.NewError(model.CodeInfeasible, false,
			"constraints unsatisfiable: %s", strings.Join(c.Reasons, "; "))
	}
	c.Feasible = true
	return c, nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
