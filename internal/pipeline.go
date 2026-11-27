package rtpreplay

import (
	"encoding/json"
	"fmt"
)

// Report is the terminal artifact: one per run, with provenance, units, and
// status for every stream. It is the JSON written to --out.
type Report struct {
	Version        string        `json:"version"`
	Case           string        `json:"case"`
	Seed           int64         `json:"seed"`
	Status         string        `json:"status"`
	ExitCode       int           `json:"exit_code"`
	Timebase       string        `json:"timebase"`
	Units          ReportUnits   `json:"units"`
	InputDigest    string        `json:"input_digest"`
	ConfigDigest   string        `json:"config_digest"`
	ArtifactSHA256 string        `json:"artifact_sha256"`
	Metrics        []*Metrics    `json:"metrics"`
	Manifest       *Manifest     `json:"manifest,omitempty"`
	Blocked        *NodeResult   `json:"blocked,omitempty"`
	Failures       []*NodeResult `json:"failures,omitempty"`
	Nodes          []*NodeResult `json:"nodes"`
	Notes          []string      `json:"notes,omitempty"`
	GeneratedBy    string        `json:"generated_by"`
}

// ReportUnits documents every unit appearing in the report.
type ReportUnits struct {
	Bitrate   string `json:"bitrate"`
	Latency   string `json:"latency"`
	Duration  string `json:"duration"`
	Alignment string `json:"alignment"`
	Quality   string `json:"quality_score"`
}

// PipelineConfig is the configuration half of the idempotency key. Changing
// any value must change ConfigDigest and therefore every artifact.
type PipelineConfig struct {
	ReorderDepth int    `json:"reorder_depth"`
	Case         string `json:"case"`
	Seed         int64  `json:"seed"`
	Version      string `json:"version"`
	FailNode     string `json:"fail_node,omitempty"`
	// FaultMode is "permanent" (non-retryable) or "transient" (retryable,
	// clears after FaultTimes failures).
	FaultMode  string `json:"fault_mode,omitempty"`
	FaultTimes int    `json:"fault_times,omitempty"`
}

// Digest returns the canonical hash of the configuration.
func (c PipelineConfig) Digest() string { return digestJSON(c) }

// Pipeline bundles the shared state for the DAG nodes.
type Pipeline struct {
	Trace      *ParsedTrace
	Config     PipelineConfig
	MP4        *MP4Track
	Notes      []string
	faultFired int
}

// nodeParse validates the trace and, when present, the MP4 companion file.
func (p *Pipeline) nodeParse(deps map[NodeID]any) (any, error) {
	if err := p.fault("parse"); err != nil {
		return nil, err
	}
	if p.Trace.MP4 != nil {
		// The MP4 bytes are generated with the trace and handed in by the
		// caller; re-reading them here proves the container timeline is
		// actually available to the regression pass.
		if p.MP4 == nil {
			return nil, NewError(CodeMP4, "parse", "trace references an MP4 that was not supplied").
				WithRetryable(false)
		}
	}
	return p.Trace, nil
}

// nodeStates reconstructs packet states for every stream.
func (p *Pipeline) nodeStates(deps map[NodeID]any) (any, error) {
	if err := p.fault("states"); err != nil {
		return nil, err
	}
	var out []*StreamState
	for i := range p.Trace.Streams {
		ps := &p.Trace.Streams[i]
		var mp4durs []uint64
		if p.MP4 != nil && p.MP4.Codec == ps.Codec && p.MP4.Timescale != 0 {
			mp4durs = make([]uint64, 0, len(p.MP4.Samples))
			for _, s := range p.MP4.Samples {
				d, err := Scale(uint64(s.Duration), p.MP4.Timescale, ps.ClockHz)
				if err != nil {
					return nil, AsError(err).WithTrack(ps.Track)
				}
				mp4durs = append(mp4durs, d)
			}
		}
		ss, err := BuildStreamState(*ps, p.Config.ReorderDepth, mp4durs)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, nil
}

// nodeMetrics computes regression metrics from the reconstructed states.
func (p *Pipeline) nodeMetrics(deps map[NodeID]any) (any, error) {
	if err := p.fault("metrics"); err != nil {
		return nil, err
	}
	states := deps["states"].([]*StreamState)
	out := make([]*Metrics, 0, len(states))
	for _, ss := range states {
		m, err := ComputeMetrics(ss, p.Trace.Events)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// nodeManifest aligns the reconstructed timeline with the declared duration.
func (p *Pipeline) nodeManifest(deps map[NodeID]any) (any, error) {
	if err := p.fault("manifest"); err != nil {
		return nil, err
	}
	states := deps["states"].([]*StreamState)
	declared := make(map[string]uint64, len(states))
	for _, ss := range states {
		declared[ss.Track] = ss.DeclaredDurationMs
	}
	return BuildManifest(states, declared)
}

// nodeReport assembles the terminal report from upstream artifacts.
func (p *Pipeline) nodeReport(deps map[NodeID]any) (any, error) {
	if err := p.fault("report"); err != nil {
		return nil, err
	}
	metrics := deps["metrics"].([]*Metrics)
	manifest := deps["manifest"].(*Manifest)
	rep := &Report{
		Version:      "1",
		Case:         p.Config.Case,
		Seed:         p.Config.Seed,
		Status:       "ok",
		ExitCode:     0,
		Timebase:     fmt.Sprintf("%d/1", p.Trace.Raw.Clock.RTPRateHz),
		InputDigest:  p.Trace.InputDigest,
		ConfigDigest: p.Config.Digest(),
		Units: ReportUnits{
			Bitrate: UnitBitsPerSec, Latency: UnitMilliseconds, Duration: UnitMilliseconds,
			Alignment: UnitMilliseconds, Quality: UnitScore,
		},
		Metrics:     metrics,
		Manifest:    manifest,
		Notes:       p.Notes,
		GeneratedBy: "rtpreplay",
	}
	return rep, nil
}

// fault implements deterministic fault injection. A transient fault is
// retryable and clears after FaultTimes failures, so the worker's retry path
// is exercised and recovery is observable; a permanent fault is non-retryable
// and must fail the node on the first attempt.
func (p *Pipeline) fault(node string) error {
	times := p.Config.FaultTimes
	if times <= 0 {
		times = 1
	}
	if p.Config.FailNode != node || p.faultFired >= times {
		return nil
	}
	p.faultFired++
	if p.Config.FaultMode == "transient" {
		return NewError(CodeInjected, node, "injected transient failure").WithRetryable(true)
	}
	return NewError(CodeInjected, node, "injected permanent failure").WithRetryable(false)
}

// BuildDag wires the five pipeline nodes.
func (p *Pipeline) BuildDag() (*Dag, error) {
	return NewDag([]*Node{
		{ID: "parse", Run: p.nodeParse},
		{ID: "states", Deps: []NodeID{"parse"}, Run: p.nodeStates, MaxRetry: 1},
		{ID: "metrics", Deps: []NodeID{"states"}, Run: p.nodeMetrics, MaxRetry: 1},
		{ID: "manifest", Deps: []NodeID{"states"}, Run: p.nodeManifest, MaxRetry: 1},
		{ID: "report", Deps: []NodeID{"metrics", "manifest"}, Run: p.nodeReport, MaxRetry: 1},
	})
}

// MarshalReport renders the report deterministically (sorted maps do not
// appear; slices are already ordered by construction).
func MarshalReport(r *Report) ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, NewError(CodeInvalidInput, "marshal_report", err.Error()).WithCause(err)
	}
	return append(b, '\n'), nil
}
