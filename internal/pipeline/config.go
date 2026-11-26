package pipeline

import (
	"fmt"
	"time"

	"replay/internal/clock"
	"replay/internal/events"
)

// Config is the validated run configuration; its digest binds checkpoints
// and cache keys so changed flags always force fresh work.
type Config struct {
	Input         string
	Events        string
	OutDir        string
	CacheDir      string
	RTPClock      uint32
	RTPAnchor     events.Anchor
	Pre           time.Duration
	Post          time.Duration
	MinConfidence float64
	Budget        time.Duration
	StopAfter     string // empty or one of scan/plan/cut
}

// ConfigMap renders the config as stable key/value pairs for digesting.
func (c *Config) ConfigMap() map[string]string {
	return map[string]string{
		"rtp_clock":      fmt.Sprintf("%d", c.RTPClock),
		"rtp_anchor":     fmt.Sprintf("%d:%d", c.RTPAnchor.RtpTick, c.RTPAnchor.MediaTick),
		"pre_ns":         fmt.Sprintf("%d", c.Pre.Nanoseconds()),
		"post_ns":        fmt.Sprintf("%d", c.Post.Nanoseconds()),
		"min_confidence": fmt.Sprintf("%.6f", c.MinConfidence),
		"budget_ns":      fmt.Sprintf("%d", c.Budget.Nanoseconds()),
	}
}

// Stats carries every acceptance metric in a stable order.
type Stats struct {
	Accepted       int
	Duplicate      int
	SkipBudget     int
	SkipConfidence int
	SkipUntrusted  int
	NoKeyframe     int
	RTPBad         int
	RTPWraps       int
	AnomalyCount   int
	Affected       int
	ClipS          float64
	BudgetS        float64
	DeviationS     float64
	CacheHits      int
	CacheRebuilds  int
}

// validate rejects bad configuration before any file is touched.
func validate(c *Config) error {
	if c.Input == "" || c.Events == "" || c.OutDir == "" {
		return clock.NewError(clock.KindConfig, "config",
			"input, events and out are required")
	}
	if c.RTPClock == 0 {
		return clock.NewError(clock.KindConfig, "config", "rtp-clock must be > 0")
	}
	if c.Pre < 0 || c.Post < 0 || c.Budget <= 0 {
		return clock.NewError(clock.KindConfig, "config",
			"pre/post must be >= 0 and budget > 0")
	}
	if c.MinConfidence < 0 || c.MinConfidence > 1 {
		return clock.NewError(clock.KindConfig, "config",
			"min-confidence must be within [0,1]")
	}
	switch c.StopAfter {
	case "", StageScan, StagePlan, StageCut:
	default:
		return clock.NewError(clock.KindConfig, "config", "unknown stop-after stage")
	}
	return nil
}