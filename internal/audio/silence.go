// Package audio detects silence windows in PCM audio and in level-annotated
// RTP streams. A track whose samples cannot be measured is reported as
// undetectable; it is never reported as silent, because a missing or undecoded
// track must not fabricate mute intervals for the editor.
package audio

import (
	"errors"
	"fmt"
	"math"

	"github.com/pion/rtp"

	"github.com/pairwise/mediad/internal/fault"
)

// Config controls windowing and the silence decision.
type Config struct {
	WindowNS      int64   // analysis window, default 20 ms
	ThresholdDBFS float64 // RMS below this is silent, default -60 dBFS
	MinSilenceNS  int64   // shortest reported silence, default 200 ms
	LevelID       uint8   // RTP audio-level extension id, default 1
	LevelSilent   uint8   // level at or above this is silent (dBov), default 60
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	return Config{WindowNS: 20_000_000, ThresholdDBFS: -60, MinSilenceNS: 200_000_000, LevelID: 1, LevelSilent: 60}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.WindowNS <= 0 {
		c.WindowNS = d.WindowNS
	}
	if c.ThresholdDBFS == 0 {
		c.ThresholdDBFS = d.ThresholdDBFS
	}
	if c.MinSilenceNS <= 0 {
		c.MinSilenceNS = d.MinSilenceNS
	}
	if c.LevelSilent == 0 {
		c.LevelSilent = d.LevelSilent
	}
	return c
}

// Range is a half-open interval of unified presentation ticks.
type Range struct{ Start, End int64 }

// Window is one measured analysis window.
type Window struct {
	Start int64
	End   int64
	RMS   float64 // linear RMS in [0,1]
	DBFS  float64
}

// Result is the silence analysis of one track.
type Result struct {
	TrackID    uint32
	Detectable bool
	Reason     string
	Windows    []Window
	Silence    []Range
	Source     string // "pcm", "rtp-audio-level" or ""
}

// Undetectable builds a result that carries no silence claims.
func Undetectable(trackID uint32, reason string) Result {
	return Result{TrackID: trackID, Detectable: false, Reason: reason}
}

// AnalyzePCM measures 16-bit mono/stereo PCM windows. sampleRate is in Hz and
// start is the presentation time of the first frame. Interleaved samples are
// averaged by channel.
func AnalyzePCM(trackID uint32, pcm []int16, channels int, sampleRate int, start int64, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	if sampleRate <= 0 {
		return Undetectable(trackID, "unknown sample rate"), nil
	}
	if channels <= 0 {
		return Undetectable(trackID, "unknown channel count"), nil
	}
	if len(pcm) == 0 {
		return Undetectable(trackID, "no audio samples"), nil
	}
	frames := len(pcm) / channels
	samplesPerWindow := int(int64(sampleRate) * cfg.WindowNS / 1e9)
	if samplesPerWindow < 1 {
		samplesPerWindow = 1
	}
	res := Result{TrackID: trackID, Detectable: true, Source: "pcm"}
	for f := 0; f < frames; f += samplesPerWindow {
		end := f + samplesPerWindow
		if end > frames {
			end = frames
		}
		var sum float64
		var n int
		for i := f; i < end; i++ {
			for c := 0; c < channels; c++ {
				v := float64(pcm[i*channels+c]) / 32768.0
				sum += v * v
				n++
			}
		}
		if n == 0 {
			continue
		}
		rms := math.Sqrt(sum / float64(n))
		ws := start + int64(f)*1e9/int64(sampleRate)
		we := start + int64(end)*1e9/int64(sampleRate)
		db := -math.Inf(1)
		if rms > 0 {
			db = 20 * math.Log10(rms)
		}
		res.Windows = append(res.Windows, Window{Start: ws, End: we, RMS: rms, DBFS: db})
	}
	res.Silence = collectSilence(res.Windows, cfg)
	return res, nil
}

func collectSilence(windows []Window, cfg Config) []Range {
	var out []Range
	var cur *Range
	for _, w := range windows {
		silent := w.DBFS <= cfg.ThresholdDBFS
		if silent {
			if cur == nil {
				cur = &Range{Start: w.Start, End: w.End}
			} else {
				cur.End = w.End
			}
			continue
		}
		if cur != nil {
			if cur.End-cur.Start >= cfg.MinSilenceNS {
				out = append(out, *cur)
			}
			cur = nil
		}
	}
	if cur != nil && cur.End-cur.Start >= cfg.MinSilenceNS {
		out = append(out, *cur)
	}
	return out
}

// AnalyzeRTP computes silence from RFC 6464 audio-level header extensions.
// The level is in dBov, where 0 is loudest and 127 is silence. Packets
// without the extension are skipped; if none carry it, the stream is
// undetectable rather than assumed silent.
func AnalyzeRTP(trackID uint32, packets []rtp.Packet, clockRate int, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	if clockRate <= 0 {
		return Undetectable(trackID, "unknown RTP clock rate"), nil
	}
	if len(packets) == 0 {
		return Undetectable(trackID, "no RTP packets"), nil
	}
	res := Result{TrackID: trackID, Source: "rtp-audio-level"}
	var seen int
	var prevEnd int64
	var cur *Range
	flush := func() {
		if cur != nil && cur.End-cur.Start >= cfg.MinSilenceNS {
			res.Silence = append(res.Silence, *cur)
		}
		cur = nil
	}
	for i := range packets {
		raw := packets[i].GetExtension(cfg.LevelID)
		if raw == nil {
			continue
		}
		var lvl rtp.AudioLevelExtension
		if err := lvl.Unmarshal(raw); err != nil {
			return res, fault.New("audio.rtp", fault.KindAudio, err).WithTrack(trackID).Recovered()
		}
		seen++
		start := int64(packets[i].Timestamp) * 1e9 / int64(clockRate)
		// Packets are assumed to arrive in decode order; lasting until the
		// next packet's timestamp, or one window if this is the last one.
		end := start + cfg.WindowNS
		if prevEnd > start {
			end = prevEnd
		}
		prevEnd = end
		db := -float64(lvl.Level)
		res.Windows = append(res.Windows, Window{Start: start, End: end, RMS: math.Pow(10, db/20), DBFS: db})
		if lvl.Level >= cfg.LevelSilent {
			if cur == nil {
				cur = &Range{Start: start, End: end}
			} else if end > cur.End {
				cur.End = end
			}
		} else {
			flush()
		}
	}
	flush()
	if seen == 0 {
		return Undetectable(trackID, fmt.Sprintf("no RTP packet carries audio-level extension %d", cfg.LevelID)).
			withSource(res.Source), nil
	}
	res.Detectable = true
	return res, nil
}

func (r Result) withSource(src string) Result { r.Source = src; return r }

// IsSilent reports whether t falls inside a reported silence interval.
func (r Result) IsSilent(t int64) bool {
	for _, s := range r.Silence {
		if t >= s.Start && t < s.End {
			return true
		}
	}
	return false
}

// Validate checks internal consistency of a result.
func (r Result) Validate() error {
	if !r.Detectable {
		if len(r.Silence) > 0 {
			return errors.New("undetectable result carries silence intervals")
		}
		return nil
	}
	prev := int64(math.MinInt64)
	for _, s := range r.Silence {
		if s.End <= s.Start {
			return fmt.Errorf("silence range [%d,%d) is empty or negative", s.Start, s.End)
		}
		if s.Start < prev {
			return fmt.Errorf("silence range [%d,%d) overlaps or is unordered", s.Start, s.End)
		}
		prev = s.End
	}
	return nil
}
