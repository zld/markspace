package jitter

import (
	"time"
)

// Config controls the adaptive jitter-buffer controller. All durations are in
// milliseconds in the public fields and converted internally.
type Config struct {
	FrameMS         int
	SampleMS        int
	MinTargetMS     float64
	MaxTargetMS     float64
	InitialTargetMS float64
	WaitMS          int
	HiddenThreshold int
	InterruptMS     int
	Queue           QueueConfig
}

// DefaultConfig returns conservative voice-friendly defaults.
func DefaultConfig() Config {
	return Config{
		FrameMS:         20,
		SampleMS:        100,
		MinTargetMS:     30,
		MaxTargetMS:     250,
		InitialTargetMS: 50,
		WaitMS:          150,
		HiddenThreshold: 3,
		InterruptMS:     1000,
		Queue:           DefaultQueueConfig(),
	}
}

func (c Config) normalized() Config {
	if c.FrameMS <= 0 {
		c.FrameMS = 20
	}
	if c.SampleMS <= 0 {
		c.SampleMS = 100
	}
	if c.MinTargetMS < 0 {
		c.MinTargetMS = 0
	}
	if c.MaxTargetMS < c.MinTargetMS {
		c.MaxTargetMS = c.MinTargetMS
	}
	if c.InitialTargetMS == 0 {
		c.InitialTargetMS = c.MinTargetMS
	}
	if c.InitialTargetMS < c.MinTargetMS {
		c.InitialTargetMS = c.MinTargetMS
	}
	if c.InitialTargetMS > c.MaxTargetMS {
		c.InitialTargetMS = c.MaxTargetMS
	}
	if c.WaitMS <= 0 {
		c.WaitMS = 150
	}
	if c.HiddenThreshold < 1 {
		c.HiddenThreshold = 1
	}
	if c.InterruptMS <= 0 {
		c.InterruptMS = 1000
	}
	c.Queue = c.Queue.normalized()
	return c
}

func msToDur(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}

func durToMS(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
