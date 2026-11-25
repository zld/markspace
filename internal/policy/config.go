// Package policy turns track metadata, quality measurements, and bitrate or
// byte budgets into explainable conversion configuration.
package policy

import (
	"fmt"

	"archivecheck/internal/clock"
	"archivecheck/internal/diagnostic"
	"archivecheck/internal/media"
)

const containerOverheadBytes = int64(4096)

// Quality contains the measured quality signal used to choose a bitrate.
type Quality struct {
	Score            int   `json:"score"`
	SourceBitrateBps int64 `json:"source_bitrate_bps"`
}

// Constraints are the conversion target and hard limits.
type Constraints struct {
	QualityFloor  int                   `json:"quality_floor"`
	MaxBitrateBps int64                 `json:"max_bitrate_bps,omitempty"`
	BudgetBytes   int64                 `json:"budget_bytes,omitempty"`
	DurationNS    clock.Nanoseconds     `json:"duration_ns,omitempty"`
	SourceOffset  int64                 `json:"source_offset"`
	Layout        media.RequestedLayout `json:"layout,omitempty"`
}

// Params are selected deterministic conversion parameters.
type Params struct {
	Profile         string `json:"profile"`
	VideoCodec      string `json:"video_codec"`
	AudioCodec      string `json:"audio_codec"`
	Width           uint32 `json:"width,omitempty"`
	Height          uint32 `json:"height,omitempty"`
	VideoBitrateBps int64  `json:"video_bitrate_bps"`
	AudioBitrateBps int64  `json:"audio_bitrate_bps"`
	Channels        uint16 `json:"channels,omitempty"`
	SampleRate      uint32 `json:"sample_rate,omitempty"`
}

// Config is the evidenced conversion plan.
type Config struct {
	Sample             int              `json:"sample"`
	Feasible           bool             `json:"feasible"`
	QualityTarget      int              `json:"quality_target"`
	QualityScore       int              `json:"quality_score"`
	MaxBitrateBps      int64            `json:"max_bitrate_bps,omitempty"`
	BudgetBytes        int64            `json:"budget_bytes,omitempty"`
	DurationNS         int64            `json:"duration_ns"`
	RequiredBitrateBps int64            `json:"required_bitrate_bps"`
	MinimumBitrateBps  int64            `json:"minimum_bitrate_bps"`
	EstimatedBytes     int64            `json:"estimated_bytes"`
	Selected           Params           `json:"selected"`
	Reasons            []string         `json:"reasons"`
	Layout             media.LayoutPlan `json:"layout"`
}

// Decide produces a configuration or an infeasible result carrying a structured
// BUDGET_INFEASIBLE error. Metadata conflicts retain their layout phase/code.
func Decide(sample int, desc media.TrackDescription, quality Quality, constraints Constraints) (Config, error) {
	layout, layoutErr := media.ResolveLayout(sample, desc, constraints.Layout)
	cfg := Config{
		Sample:        sample,
		Feasible:      layoutErr == nil,
		QualityScore:  quality.Score,
		QualityTarget: constraints.QualityFloor,
		BudgetBytes:   constraints.BudgetBytes,
		Layout:        layout,
	}
	if cfg.QualityTarget == 0 {
		cfg.QualityTarget = 80
	}
	for _, d := range layout.Decisions {
		cfg.Reasons = append(cfg.Reasons, fmt.Sprintf("layout/%s=%s: %s", d.Field, d.Action, d.Reason))
	}
	if layoutErr != nil {
		cfg.Reasons = append(cfg.Reasons, "configuration rejected because metadata layout is not safe")
		return cfg, layoutErr
	}

	duration := constraints.DurationNS
	if duration == 0 {
		duration = clock.Nanoseconds(desc.DurationNS)
	}
	if duration <= 0 {
		duration = clock.Second
	}
	cfg.DurationNS = int64(duration)

	params := selectParameters(desc, quality, cfg.QualityTarget)
	applyRequestedLayout(&params, desc, constraints.Layout)
	cfg.Selected = params
	cfg.RequiredBitrateBps = params.VideoBitrateBps + params.AudioBitrateBps
	cfg.Reasons = append(cfg.Reasons, qualityReason(quality, cfg.QualityTarget, params.Profile))

	capBps, capReason := effectiveBitrateCap(duration, constraints)
	if capBps > 0 {
		cfg.MaxBitrateBps = capBps
		cfg.Reasons = append(cfg.Reasons, capReason)
	}

	minimumVideo := int64(0)
	if params.VideoCodec != "" {
		minimumVideo = 250_000
	}
	cfg.MinimumBitrateBps = params.AudioBitrateBps + minimumVideo

	if capBps > 0 && cfg.MinimumBitrateBps > capBps {
		cfg.Feasible = false
		cfg.EstimatedBytes = estimateBytes(cfg.RequiredBitrateBps, duration)
		cfg.Reasons = append(cfg.Reasons, fmt.Sprintf(
			"minimum quality bitrate %d bps exceeds cap %d bps; desired estimate %d bytes",
			cfg.MinimumBitrateBps, capBps, cfg.EstimatedBytes,
		))
		return cfg, diagnostic.New(diagnostic.CodeBudgetInfeasible, diagnostic.PhasePolicy, sample,
			constraints.SourceOffset, "budget/bitrate cannot meet quality floor %d", cfg.QualityTarget)
	}

	if capBps > 0 {
		total := cfg.RequiredBitrateBps
		if total > capBps {
			cfg.Reasons = append(cfg.Reasons, fmt.Sprintf(
				"cap reduces desired bitrate from %d to %d while staying above minimum %d",
				cfg.RequiredBitrateBps, capBps, cfg.MinimumBitrateBps,
			))
			total = capBps
		}
		params.VideoBitrateBps = max(minimumVideo, total-params.AudioBitrateBps)
		params.AudioBitrateBps = total - params.VideoBitrateBps
		cfg.Selected = params
	}
	cfg.EstimatedBytes = estimateBytes(params.VideoBitrateBps+params.AudioBitrateBps, duration)
	cfg.Feasible = true
	cfg.Reasons = append(cfg.Reasons, fmt.Sprintf(
		"selected profile=%s video_codec=%s audio_codec=%s size=%dx%d video=%d_bps audio=%d_bps channels=%d sample_rate=%d estimate=%d_bytes",
		params.Profile, params.VideoCodec, params.AudioCodec, params.Width, params.Height,
		params.VideoBitrateBps, params.AudioBitrateBps, params.Channels, params.SampleRate, cfg.EstimatedBytes,
	))
	return cfg, nil
}

func selectParameters(desc media.TrackDescription, quality Quality, floor int) Params {
	p := Params{
		Width:      desc.Width,
		Height:     desc.Height,
		Channels:   desc.Channels,
		SampleRate: desc.SampleRate,
		AudioCodec: "aac-lc",
		VideoCodec: "h264-high",
	}
	if p.Channels == 0 {
		p.Channels = 2
	}
	if p.SampleRate == 0 {
		p.SampleRate = 48000
	}
	switch {
	case p.Channels <= 1:
		p.AudioBitrateBps = 64_000
	case p.Channels <= 2:
		p.AudioBitrateBps = 128_000
	default:
		p.AudioBitrateBps = 256_000
	}

	if desc.Kind == "subtitle" || desc.Kind == "timecode" || desc.Kind == "metadata" {
		p.Profile = desc.Kind + "-archive-v1"
		p.VideoCodec = ""
		p.AudioCodec = ""
		p.Channels = 0
		p.SampleRate = 0
		p.AudioBitrateBps = 0
		p.VideoBitrateBps = 0
		return p
	}
	if desc.Kind == "audio" || desc.Width == 0 {
		p.Profile = "audio-archive-v1"
		p.VideoCodec = ""
		p.VideoBitrateBps = 0
		return p
	}

	switch {
	case quality.Score < floor:
		p.Profile = "quality-preserve-v1"
		p.VideoBitrateBps = 1_200_000
		if quality.SourceBitrateBps > p.VideoBitrateBps {
			p.VideoBitrateBps = quality.SourceBitrateBps
		}
	case quality.Score >= 90:
		p.Profile = "high-quality-proxy-v1"
		p.VideoBitrateBps = 500_000
	default:
		p.Profile = "balanced-archive-v1"
		p.VideoBitrateBps = 800_000
	}
	return p
}

func applyRequestedLayout(p *Params, source media.TrackDescription, req media.RequestedLayout) {
	if req.Channels != 0 {
		p.Channels = req.Channels
	} else if req.ChannelLayout != "" {
		switch req.ChannelLayout {
		case media.LayoutMono:
			p.Channels = 1
		case media.LayoutStereo:
			p.Channels = 2
		case media.Layout51:
			p.Channels = 6
		case media.Layout71:
			p.Channels = 8
		}
	}
	if req.SampleRate != 0 {
		p.SampleRate = req.SampleRate
	}
	_ = source
}

func qualityReason(q Quality, floor int, profile string) string {
	if q.Score < floor {
		return fmt.Sprintf("quality score %d below floor %d; profile %s avoids generation loss", q.Score, floor, profile)
	}
	return fmt.Sprintf("quality score %d meets floor %d; selected profile %s", q.Score, floor, profile)
}

func effectiveBitrateCap(duration clock.Nanoseconds, c Constraints) (int64, string) {
	var cap int64
	var reason string
	if c.MaxBitrateBps > 0 {
		cap = c.MaxBitrateBps
		reason = fmt.Sprintf("explicit bitrate cap %d bps", cap)
	}
	if c.BudgetBytes > 0 && duration > 0 {
		usable := c.BudgetBytes - containerOverheadBytes
		if usable < 0 {
			return 1, fmt.Sprintf("budget %d below container overhead %d", c.BudgetBytes, containerOverheadBytes)
		}
		budgetCap := (usable * 8 * int64(clock.Second)) / int64(duration)
		if cap == 0 || budgetCap < cap {
			cap = budgetCap
			reason = fmt.Sprintf("budget %d bytes over %dms implies %d bps after %d-byte overhead",
				c.BudgetBytes, duration/clock.Millisecond, cap, containerOverheadBytes)
		}
	}
	return cap, reason
}

func estimateBytes(totalBitrateBps int64, duration clock.Nanoseconds) int64 {
	den := 8 * int64(clock.Second)
	payload := (totalBitrateBps*int64(duration) + den - 1) / den
	return payload + containerOverheadBytes
}
