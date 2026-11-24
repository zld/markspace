// Package model contains the shared media time base and the unified fault
// model used by ingest, recovery, synchronization, checkpointing and reporting.
package model

import (
	"fmt"
	"time"
)

// VideoTimescale is the deterministic demo time base: 90 kHz, 25 fps.
const (
	VideoTimescale uint32 = 90000
	VideoFrameDelta       = 3600 // 40 ms in 90 kHz
	AudioClockRate uint32 = 48000
)

// Timebase is a rational mapping from media ticks to wall-clock duration.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// NewTimebase returns a time base and rejects a zero denominator.
func NewTimebase(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, fmt.Errorf("model: timescale must be positive")
	}
	return Timebase{Timescale: timescale}, nil
}

// PTS converts a duration to integer ticks.
func (t Timebase) PTS(d time.Duration) uint64 {
	if t.Timescale == 0 {
		return 0
	}
	return uint64((d.Nanoseconds()*int64(t.Timescale) + int64(time.Second)/2) / int64(time.Second))
}

// Duration converts ticks to time.Duration.
func (t Timebase) Duration(pts uint64) time.Duration {
	if t.Timescale == 0 {
		return 0
	}
	return time.Duration(int64(pts) * int64(time.Second) / int64(t.Timescale))
}

// FrameIndex maps a PTS to the nearest frame-grid index for frameDelta ticks.
func (t Timebase) FrameIndex(pts uint64, frameDelta uint32) int64 {
	if frameDelta == 0 {
		return 0
	}
	return int64((pts + uint64(frameDelta)/2) / uint64(frameDelta))
}

// ClockMap maps one declared clock to the shared media time base. The zero
// value is an identity map when SourceRate and Media.Timescale are equal.
type ClockMap struct {
	Name         string   `json:"name"`
	SourceRate   uint32   `json:"source_rate"`
	Media        Timebase `json:"media"`
	OriginSource uint64   `json:"origin_source"`
	OriginMedia  uint64   `json:"origin_media"`
	// StartOffset is added after rate conversion. Positive moves media later.
	StartOffset time.Duration `json:"start_offset_ns"`
}

// Map converts a source-clock timestamp to shared media PTS.
func (m ClockMap) Map(sourceTS uint64) uint64 {
	rate := m.SourceRate
	if rate == 0 {
		rate = m.Media.Timescale
	}
	mediaRate := m.Media.Timescale
	if mediaRate == 0 {
		mediaRate = rate
	}
	delta := int64(sourceTS) - int64(m.OriginSource)
	scaled := delta * int64(mediaRate) / int64(rate)
	offsetTicks := int64(m.Media.PTS(m.StartOffset))
	return uint64(int64(m.OriginMedia) + scaled + offsetTicks)
}

// AVSync reports whether audio and video times differ by at most one frame.
type AVSync struct {
	VideoPTS       uint64        `json:"video_pts"`
	AudioPTS       uint64        `json:"audio_pts"`
	Delta          time.Duration `json:"delta_ns"`
	FramePeriod    time.Duration `json:"frame_period_ns"`
	Tolerance      time.Duration `json:"tolerance_ns"`
	WithinFrame    bool          `json:"within_frame"`
	OriginalVideo  uint64        `json:"original_video_pts"`
	OriginalAudio  uint64        `json:"original_audio_pts"`
}

// CheckAVSync compares mapped audio PTS with the nearest video PTS. On false
// the caller must retain both original timestamps instead of rewriting them.
func CheckAVSync(videoPTS, audioPTS uint64, tb Timebase, frameDelta uint32) AVSync {
	period := tb.Duration(uint64(frameDelta))
	v := tb.Duration(videoPTS)
	a := tb.Duration(audioPTS)
	delta := a - v
	if delta < 0 {
		delta = -delta
	}
	return AVSync{
		VideoPTS:      videoPTS,
		AudioPTS:      audioPTS,
		Delta:         delta,
		FramePeriod:   period,
		Tolerance:     period,
		WithinFrame:   delta <= period,
		OriginalVideo: videoPTS,
		OriginalAudio: audioPTS,
	}
}
