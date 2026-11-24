package model

import "fmt"

// Limits bounds untrusted inputs and in-memory replay state.
type Limits struct {
	MaxInputBytes     int64 `json:"max_input_bytes"`
	MaxBoxDepth       int   `json:"max_box_depth"`
	MaxBufferedEvents int   `json:"max_buffered_events"`
	MaxCacheWindowNS  NS    `json:"max_cache_window_ns"`
}

// DefaultLimits returns conservative bounds for local archive processing.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:     64 << 20,
		MaxBoxDepth:       32,
		MaxBufferedEvents: 4096,
		MaxCacheWindowNS:  30 * Second,
	}
}

// Anchor is a stable correspondence between a track-local corrected clock and
// the shared program clock.
type Anchor struct {
	TrackID  uint32 `json:"track_id"`
	SourceNS NS     `json:"source_ns"`
	SharedNS NS     `json:"shared_ns"`
	Stable   bool   `json:"stable"`
}

// Config contains every tunable and tolerance that affects a result.
type Config struct {
	Seed                int64    `json:"seed"`
	ReferenceTrackID    uint32   `json:"reference_track_id"`
	SegmentDurationNS   NS       `json:"segment_duration_ns"`
	BoundaryToleranceNS NS       `json:"boundary_tolerance_ns"`
	DriftMaxPPM         int64    `json:"drift_max_ppm"`
	AnchorToleranceNS   NS       `json:"anchor_tolerance_ns"`
	AllowedLatenessNS   NS       `json:"allowed_lateness_ns"`
	WrapBits            int      `json:"wrap_bits"`
	KeepPartialTail     bool     `json:"keep_partial_tail"`
	Anchors             []Anchor `json:"anchors,omitempty"`
	Limits              Limits   `json:"limits"`
}

// DefaultConfig is the deterministic demo and test configuration.
func DefaultConfig() Config {
	return Config{
		ReferenceTrackID:    1,
		SegmentDurationNS:   2 * Second,
		BoundaryToleranceNS: 25 * Millisecond,
		DriftMaxPPM:         20_000, // two percent, broad enough for self-test drift
		AnchorToleranceNS:   5 * Millisecond,
		AllowedLatenessNS:   500 * Millisecond,
		WrapBits:            32,
		KeepPartialTail:     true,
		Limits:              DefaultLimits(),
	}
}

// Validate checks parameter ranges before touching untrusted input.
func (c Config) Validate() error {
	if c.SegmentDurationNS <= 0 {
		return &ConfigError{Field: "segment_duration_ns", Value: c.SegmentDurationNS, Err: fmt.Errorf("must be positive")}
	}
	if c.BoundaryToleranceNS < 0 {
		return &ConfigError{Field: "boundary_tolerance_ns", Value: c.BoundaryToleranceNS, Err: fmt.Errorf("must be non-negative")}
	}
	if c.DriftMaxPPM < 0 || c.DriftMaxPPM > 1_000_000 {
		return &ConfigError{Field: "drift_max_ppm", Value: c.DriftMaxPPM, Err: fmt.Errorf("must be in [0,1000000]")}
	}
	if c.AllowedLatenessNS < 0 {
		return &ConfigError{Field: "allowed_lateness_ns", Value: c.AllowedLatenessNS, Err: fmt.Errorf("must be non-negative")}
	}
	if c.WrapBits != 0 && (c.WrapBits < 8 || c.WrapBits > 64) {
		return &ConfigError{Field: "wrap_bits", Value: c.WrapBits, Err: fmt.Errorf("must be 0(box width), or in [8,64]")}
	}
	if c.Limits.MaxInputBytes <= 0 {
		return &ConfigError{Field: "limits.max_input_bytes", Value: c.Limits.MaxInputBytes, Err: fmt.Errorf("must be positive")}
	}
	if c.Limits.MaxBoxDepth <= 0 || c.Limits.MaxBoxDepth > 128 {
		return &ConfigError{Field: "limits.max_box_depth", Value: c.Limits.MaxBoxDepth, Err: fmt.Errorf("must be in [1,128]")}
	}
	if c.Limits.MaxBufferedEvents <= 0 {
		return &ConfigError{Field: "limits.max_buffered_events", Value: c.Limits.MaxBufferedEvents, Err: fmt.Errorf("must be positive")}
	}
	if c.Limits.MaxCacheWindowNS <= 0 {
		return &ConfigError{Field: "limits.max_cache_window_ns", Value: c.Limits.MaxCacheWindowNS, Err: fmt.Errorf("must be positive")}
	}
	if c.AllowedLatenessNS > c.Limits.MaxCacheWindowNS {
		return &ConfigError{
			Field: "allowed_lateness_ns",
			Value: c.AllowedLatenessNS,
			Err:   fmt.Errorf("exceeds max_cache_window_ns=%d", c.Limits.MaxCacheWindowNS),
		}
	}
	trackAnchors := map[uint32][]Anchor{}
	for _, a := range c.Anchors {
		if !a.Stable {
			continue
		}
		trackAnchors[a.TrackID] = append(trackAnchors[a.TrackID], a)
	}
	for trackID, anchors := range trackAnchors {
		if len(anchors) < 2 {
			return &ConfigError{Field: "anchors", Value: trackID, Err: fmt.Errorf("track %d needs at least two stable anchors", trackID)}
		}
	}
	return nil
}
