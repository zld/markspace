package track

import (
	"fmt"

	"regional.example/mediad/internal/fault"
	"regional.example/mediad/internal/manifest"
)

// StepKind names one ordered unit of work in a plan.
type StepKind string

const (
	// StepRepairIndex rebuilds a damaged or absent sample index.
	StepRepairIndex StepKind = "repair-index"
	// StepReadSamples streams confirmed PCM samples for a track.
	StepReadSamples StepKind = "read-samples"
	// StepResample converts a track to the output rate.
	StepResample StepKind = "resample"
	// StepLayout normalises the channel layout.
	StepLayout StepKind = "layout"
	// StepLoudness measures and applies gain with peak limiting.
	StepLoudness StepKind = "loudness"
	// StepRTPSummary produces packet timestamps and payload summaries.
	StepRTPSummary StepKind = "rtp-summary"
	// StepEmitManifest writes the forwarding manifest.
	StepEmitManifest StepKind = "emit-manifest"
)

// PlanStep is one scheduled unit of work.
type PlanStep struct {
	Kind    StepKind
	TrackID uint32 // zero for global steps
	// Degraded notes that this step runs without its full input set.
	Degraded bool
	// Note explains any degradation or skip decision affecting this step.
	Note string
}

// Plan is the executable schedule plus the decisions behind it.
type Plan struct {
	Steps []PlanStep
	// Skipped records nodes excluded from processing, with reasons.
	Skipped []Node
	// Degraded records nodes processed with reduced input, with reasons.
	Degraded []Node
	// Executable is false when the plan must not run (a cycle or a missing
	// required track was detected before the call).
	Executable bool
}

// BuildPlan derives the ordered plan from a graph.  The audio pipeline for
// each processable track follows the dependency order of the design:
// repair index, read samples, resample, layout, loudness; packet and manifest
// steps run once after all tracks have been processed.
func BuildPlan(g *Graph) (*Plan, error) {
	if g == nil {
		return nil, fault.Fatal(fault.ClassDependency, "nil-graph", "no dependency graph")
	}
	p := &Plan{Executable: true}
	p.Skipped = g.Skipped()
	p.Degraded = g.Degraded()

	order := g.Order()
	for _, id := range order {
		n, ok := g.NodeByID(id)
		if !ok || n.State == StateSkipped {
			continue
		}
		// Only audio tracks carry the PCM pipeline; other roles produce no
		// media in this library and are explicitly noted.
		if n.Decl.Role != manifest.RoleAudio {
			p.Steps = append(p.Steps, PlanStep{
				Kind: StepReadSamples, TrackID: id, Degraded: true,
				Note: fmt.Sprintf("track %d is a %s track; no PCM pipeline applies", id, n.Decl.Role),
			})
			continue
		}
		note := n.Reason
		degraded := n.State == StateDegraded
		p.Steps = append(p.Steps,
			PlanStep{Kind: StepRepairIndex, TrackID: id, Degraded: degraded, Note: note},
			PlanStep{Kind: StepReadSamples, TrackID: id, Degraded: degraded, Note: note},
			PlanStep{Kind: StepResample, TrackID: id, Degraded: degraded, Note: note},
			PlanStep{Kind: StepLayout, TrackID: id, Degraded: degraded, Note: note},
			PlanStep{Kind: StepLoudness, TrackID: id, Degraded: degraded, Note: note},
		)
	}
	// Global tail steps share the timeline.
	p.Steps = append(p.Steps,
		PlanStep{Kind: StepRTPSummary},
		PlanStep{Kind: StepEmitManifest},
	)
	return p, nil
}

// HasStep reports whether a step of the given kind and track is scheduled.
func (p *Plan) HasStep(kind StepKind, trackID uint32) bool {
	for _, s := range p.Steps {
		if s.Kind == kind && s.TrackID == trackID {
			return true
		}
	}
	return false
}

// StepsFor returns all scheduled steps for one track, in order.
func (p *Plan) StepsFor(trackID uint32) []PlanStep {
	var out []PlanStep
	for _, s := range p.Steps {
		if s.TrackID == trackID {
			out = append(out, s)
		}
	}
	return out
}

// Summary renders the plan as deterministic human-readable lines.
func (p *Plan) Summary() []string {
	var lines []string
	for _, s := range p.Steps {
		line := fmt.Sprintf("%s track=%d", s.Kind, s.TrackID)
		if s.Degraded {
			line += " degraded"
		}
		if s.Note != "" {
			line += " (" + s.Note + ")"
		}
		lines = append(lines, line)
	}
	for _, n := range p.Skipped {
		lines = append(lines, fmt.Sprintf("skip track=%d (%s)", n.Decl.ID, n.Reason))
	}
	return lines
}
