package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"continuity/internal/manifest/policy"
	"continuity/internal/media"
)

// InputDigest identifies one generated source artifact in a config package.
type InputDigest struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Reason is one machine-readable explanation for a configuration choice.
type Reason struct {
	Code    string `json:"code"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

// BitrateCandidate records why a profile was selected or rejected.
type BitrateCandidate struct {
	Name             string `json:"name"`
	TargetBitrateBPS int    `json:"target_bitrate_bps"`
	EstimatedMS      int64  `json:"estimated_ms"`
	Usable           bool   `json:"usable"`
	Reason           string `json:"reason"`
}

// BitrateDecision is the explainable target-bitrate solver result.
type BitrateDecision struct {
	SourceBitrateBPS int                `json:"source_bitrate_bps"`
	TargetBPS        int                `json:"target_bitrate_bps"`
	MaxBPS           int                `json:"max_bitrate_bps"`
	SelectedProfile  string             `json:"selected_profile,omitempty"`
	Candidates       []BitrateCandidate `json:"candidates"`
	Reason           string             `json:"reason"`
}

// BudgetDecision records the deterministic local processing estimate.
type BudgetDecision struct {
	ClusterCount int    `json:"cluster_count"`
	EventCount   int    `json:"event_count"`
	EstimatedMS  int64  `json:"estimated_ms"`
	BudgetMS     int64  `json:"budget_ms"`
	Feasible     bool   `json:"feasible"`
	Reason       string `json:"reason"`
}

// ConfigPackage is the canonical, reproducible conversion configuration.
type ConfigPackage struct {
	SchemaVersion     string            `json:"schema_version"`
	Seed              int64             `json:"seed"`
	InputPrefixSHA256 string            `json:"input_prefix_sha256,omitempty"`
	Inputs            []InputDigest     `json:"inputs,omitempty"`
	CoveragePPM       int               `json:"coverage_ppm"`
	MaxAVSkewNS       int64             `json:"max_av_skew_ns"`
	MaxAVSkewMS       float64           `json:"max_av_skew_ms"`
	Audio             *policy.AudioPlan `json:"audio,omitempty"`
	Color             *policy.ColorPlan `json:"color,omitempty"`
	Bitrate           BitrateDecision   `json:"bitrate"`
	Budget            BudgetDecision    `json:"budget"`
	Constraints       Constraints       `json:"constraints"`
	Reasons           []Reason          `json:"reasons"`
}

// PlanRequest joins canonical metadata with measured timeline quality.
type PlanRequest struct {
	Manifest     *Manifest
	Report       media.TimelineReport
	ClusterCount int
	Inputs       []InputDigest
	InputPrefix  string
}

// Plan resolves policies and validates quality/bitrate/budget constraints.
func Plan(req PlanRequest) (*ConfigPackage, error) {
	if req.Manifest == nil {
		return nil, media.NewError(media.StagePlanner, -1, "manifest", media.ErrMigration).WithKind("nil_manifest")
	}
	m := req.Manifest
	pkg := &ConfigPackage{
		SchemaVersion:     CanonicalVersion,
		Seed:              m.Seed,
		InputPrefixSHA256: req.InputPrefix,
		Inputs:            req.Inputs,
		CoveragePPM:       req.Report.CoveragePPM,
		MaxAVSkewNS:       req.Report.MaxAVSkewNS,
		MaxAVSkewMS:       req.Report.MaxAVSkewMS,
		Constraints:       m.Constraints,
	}

	audioTracks := make([]policy.AudioTrack, 0, len(m.Tracks))
	sourceBitrate := 0
	for _, t := range m.Tracks {
		audioTracks = append(audioTracks, policy.AudioTrack{
			ID:               t.ID,
			Channels:         t.Channels,
			Layout:           t.Layout,
			LayoutKnown:      t.LayoutKnown,
			InputSampleRate:  t.InputSampleRate,
			TargetSampleRate: t.TargetSampleRate,
		})
		if t.Type == "video" && t.SourceBitrateBPS > sourceBitrate {
			sourceBitrate = t.SourceBitrateBPS
		}
	}
	audioPlan, err := policy.PlanAudio(audioTracks, policy.TargetSampleRateHz, m.DefaultLFEMix)
	if err != nil {
		return pkg, err
	}
	pkg.Audio = audioPlan
	for _, mp := range audioPlan.Mappings {
		pkg.Reasons = append(pkg.Reasons, Reason{Code: "audio_matrix", Subject: fmt.Sprintf("track:%d", mp.TrackID), Message: mp.Reason})
	}
	for _, sr := range audioPlan.SampleRates {
		pkg.Reasons = append(pkg.Reasons, Reason{Code: "sample_rate", Subject: fmt.Sprintf("track:%d", sr.TrackID), Message: sr.Reason})
	}

	colorObs := make([]policy.ColorObservation, 0, len(m.ColorObservations))
	for _, o := range m.ColorObservations {
		colorObs = append(colorObs, policy.ColorObservation{Field: o.Field, Value: o.Value, Level: string(o.Level), Source: o.Source})
	}
	colorPlan, err := policy.ResolveColor(colorObs, m.ExplicitColor)
	if err != nil {
		return pkg, err
	}
	pkg.Color = colorPlan
	for _, d := range colorPlan.Decisions {
		pkg.Reasons = append(pkg.Reasons, Reason{Code: "color_" + d.Field, Subject: d.Source, Message: d.Reason})
	}

	c := m.Constraints
	if c.MinCoveragePPM > 0 && req.Report.CoveragePPM < c.MinCoveragePPM {
		err := media.NewError(media.StagePlanner, -1, "coverage",
			fmt.Errorf("%w: actual=%d required=%d", media.ErrCoverage, req.Report.CoveragePPM, c.MinCoveragePPM))
		pkg.Reasons = append(pkg.Reasons, Reason{Code: "coverage_rejected", Subject: "timeline", Message: err.Error()})
		return pkg, err
	}
	pkg.Reasons = append(pkg.Reasons, Reason{
		Code:    "coverage_accepted",
		Subject: "timeline",
		Message: fmt.Sprintf("coverage %d ppm meets minimum %d ppm", req.Report.CoveragePPM, c.MinCoveragePPM),
	})

	if c.MaxAVSkewMS > 0 && req.Report.MaxAVSkewNS > int64(c.MaxAVSkewMS)*1_000_000 {
		err := media.NewError(media.StagePlanner, -1, "av_skew",
			fmt.Errorf("%w: actual_ns=%d limit_ns=%d", media.ErrAVSkew, req.Report.MaxAVSkewNS, int64(c.MaxAVSkewMS)*1_000_000))
		pkg.Reasons = append(pkg.Reasons, Reason{Code: "skew_rejected", Subject: "timeline", Message: err.Error()})
		return pkg, err
	}
	pkg.Reasons = append(pkg.Reasons, Reason{
		Code:    "av_skew_accepted",
		Subject: "timeline",
		Message: fmt.Sprintf("maximum nearest A/V skew %.3f ms is within %d ms", req.Report.MaxAVSkewMS, c.MaxAVSkewMS),
	})

	bitrate, budget, err := solveBitrate(c, sourceBitrate, req.Report.EventCount, req.ClusterCount)
	pkg.Bitrate = bitrate
	pkg.Budget = budget
	for _, cand := range bitrate.Candidates {
		code := "profile_rejected"
		if cand.Usable {
			code = "profile_usable"
		}
		pkg.Reasons = append(pkg.Reasons, Reason{Code: code, Subject: cand.Name, Message: cand.Reason})
	}
	if err != nil {
		return pkg, err
	}
	pkg.Reasons = append(pkg.Reasons, Reason{Code: "bitrate_selected", Subject: bitrate.SelectedProfile, Message: bitrate.Reason})
	return pkg, nil
}

// CanonicalJSON returns deterministic JSON suitable for SHA-256 hashing.
func (p *ConfigPackage) CanonicalJSON() ([]byte, error) {
	return json.Marshal(p)
}

// SHA256 returns the lowercase 64-hex-character config package checksum.
func (p *ConfigPackage) SHA256() (string, error) {
	data, err := p.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func solveBitrate(c Constraints, sourceBitrate, eventCount, clusterCount int) (BitrateDecision, BudgetDecision, error) {
	if sourceBitrate <= 0 {
		sourceBitrate = 3_200_000
	}
	profiles := []struct {
		name    string
		bitrate int
		cost    int64
	}{
		{"stream_copy", sourceBitrate, 1 + int64(clusterCount)},
		{"standard_transcode", 2_200_000, 8 + int64(eventCount)/10 + int64(clusterCount)},
		{"economy_transcode", 1_200_000, 12 + int64(eventCount)/5 + int64(clusterCount)},
	}
	bd := BitrateDecision{SourceBitrateBPS: sourceBitrate, MaxBPS: c.MaxBitrateBPS}
	budget := BudgetDecision{ClusterCount: clusterCount, EventCount: eventCount, BudgetMS: c.BudgetMS, Feasible: true}

	bestIdx := -1
	for i, p := range profiles {
		reject := ""
		usable := true
		if c.MaxBitrateBPS > 0 && p.bitrate > c.MaxBitrateBPS {
			usable = false
			reject = fmt.Sprintf("bitrate %d bps exceeds cap %d bps", p.bitrate, c.MaxBitrateBPS)
		}
		if c.BudgetMS > 0 && p.cost > c.BudgetMS {
			usable = false
			if reject != "" {
				reject += "; "
			}
			reject += fmt.Sprintf("estimated cost %d ms exceeds budget %d ms", p.cost, c.BudgetMS)
		}
		if reject == "" {
			reject = fmt.Sprintf("bitrate %d bps and estimated cost %d ms satisfy both limits", p.bitrate, p.cost)
		}
		bd.Candidates = append(bd.Candidates, BitrateCandidate{Name: p.name, TargetBitrateBPS: p.bitrate, EstimatedMS: p.cost, Usable: usable, Reason: reject})
		if usable && (bestIdx == -1 || p.bitrate > profiles[bestIdx].bitrate) {
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		budget.Feasible = false
		budget.Reason = "no profile satisfies both bitrate cap and processing budget"
		err := media.NewError(media.StagePlanner, -1, "bitrate_budget", media.ErrInfeasible)
		return bd, budget, err
	}
	chosen := profiles[bestIdx]
	bd.TargetBPS = chosen.bitrate
	bd.SelectedProfile = chosen.name
	bd.Reason = fmt.Sprintf("highest-bitrate feasible profile at %d bps with estimated %d ms processing", chosen.bitrate, chosen.cost)
	budget.EstimatedMS = chosen.cost
	budget.Feasible = c.BudgetMS == 0 || chosen.cost <= c.BudgetMS
	budget.Reason = bd.Reason
	return bd, budget, nil
}
