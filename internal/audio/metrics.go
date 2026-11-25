// Package audio produces deterministic PCM sample statistics from the
// committed timeline. There is no audio device: samples are synthesised from
// event identity and time, then peak and RMS loudness are measured. Peak
// loudness is always a finite value so the manifest never emits NaN or Inf.
package audio

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"archive.example/mediakernel/internal/clock"
	"archive.example/mediakernel/internal/events"
)

// Config fixes the PCM geometry.
type Config struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	StartNS       int64
	EndNS         int64
	MaxSamples    int
}

// DefaultConfig is 48kHz stereo 16-bit for an 8 second interval.
func DefaultConfig(startNS, endNS int64) Config {
	return Config{SampleRate: 48000, Channels: 2, BitsPerSample: 16, StartNS: startNS, EndNS: endNS, MaxSamples: 1 << 20}
}

// Stats is the measured result of one PCM export.
type Stats struct {
	Samples       int
	SampleRate    int
	Channels      int
	BitsPerSample int
	Peak          float64 // absolute peak, normalised to [-1,1]
	PeakDBFS      float64
	RMS           float64
	RMSDBFS       float64
	Finite        bool
	SHA256        string
}

func (c Config) validate() (Config, *events.Error) {
	if c.SampleRate <= 0 {
		return c, events.NewError(events.CodeInternal, events.TrackAudio, 0, -1, "audio", "non-positive sample rate", nil)
	}
	if c.Channels <= 0 || c.Channels > 8 {
		return c, events.NewError(events.CodeInternal, events.TrackAudio, 0, -1, "audio", "unsupported channel count", nil)
	}
	if c.BitsPerSample != 16 {
		return c, events.NewError(events.CodeInternal, events.TrackAudio, 0, -1, "audio", "only 16-bit PCM is supported", nil)
	}
	if c.EndNS < c.StartNS {
		return c, events.NewError(events.CodeTimeOutOfRange, events.TrackAudio, 0, -1, "audio", "end before start", nil)
	}
	if c.MaxSamples <= 0 {
		c.MaxSamples = 1 << 20
	}
	return c, nil
}

// RenderSamples synthesises PCM bytes for the interval and returns them with
// their statistics. Samples are a pure function of the committed events and
// the sample index, so repeated runs are bit-identical.
func RenderSamples(cfg Config, committed []events.Event) ([]byte, Stats, *events.Error) {
	cfg, err := cfg.validate()
	if err != nil {
		return nil, Stats{}, err
	}
	spanNS := cfg.EndNS - cfg.StartNS
	if spanNS <= 0 {
		return nil, Stats{SampleRate: cfg.SampleRate, Channels: cfg.Channels,
			BitsPerSample: cfg.BitsPerSample, Finite: true}, nil
	}
	total := int(spanNS / (clock.NSPerSecond / int64(cfg.SampleRate)))
	if total > cfg.MaxSamples {
		return nil, Stats{}, events.NewError(events.CodeCapacity, events.TrackAudio, 0, -1, "audio",
			fmt.Sprintf("sample count %d exceeds limit %d", total, cfg.MaxSamples), nil)
	}
	ordered := events.SortEvents(committed)

	pcm := make([]byte, total*cfg.Channels*2)
	var peak float64
	var sumSquares float64

	for i := 0; i < total; i++ {
		at := cfg.StartNS + int64(i)*clock.NSPerSecond/int64(cfg.SampleRate)
		env := envelopeAt(ordered, at)
		for ch := 0; ch < cfg.Channels; ch++ {
			v := synthSample(i, ch, env)
			s := int16(v * 32767)
			off := (i*cfg.Channels + ch) * 2
			binary.LittleEndian.PutUint16(pcm[off:off+2], uint16(s))
			abs := math.Abs(float64(s) / 32768.0)
			if abs > peak {
				peak = abs
			}
			sumSquares += float64(s) * float64(s)
		}
	}
	samples := total * cfg.Channels
	stats := Stats{
		Samples:       total,
		SampleRate:    cfg.SampleRate,
		Channels:      cfg.Channels,
		BitsPerSample: cfg.BitsPerSample,
		Peak:          peak,
		PeakDBFS:      dbfs(peak),
		Finite:        isFinite(peak),
	}
	if samples > 0 {
		stats.RMS = math.Sqrt(sumSquares/float64(samples)) / 32768.0
		stats.RMSDBFS = dbfs(stats.RMS)
		stats.Finite = stats.Finite && isFinite(stats.RMS) && isFinite(stats.PeakDBFS) && isFinite(stats.RMSDBFS)
	}
	stats.SHA256 = hashPCM(pcm)
	return pcm, stats, nil
}

// envelopeAt returns the number of audio events active at an instant, bounded
// to a sane range so the synthetic waveform stays finite.
func envelopeAt(ordered []events.Event, at int64) int {
	n := 0
	for _, e := range ordered {
		if e.Kind != events.KindID3 && e.Track != events.TrackAudio && e.Kind != events.KindEmsg {
			continue
		}
		if e.At <= at && e.At+e.Duration > at {
			n++
		}
	}
	if n > 4 {
		n = 4
	}
	return n
}

// synthSample synthesises one sample in [-1,1) from the sample index, channel
// and envelope. It uses a deterministic integer oscillator, never a random
// source.
func synthSample(i, ch, env int) float64 {
	period := 120 + ch*40
	phase := float64((i+ch*7)%period) / float64(period)
	amp := 0.35
	if env > 0 {
		amp = 0.55 + 0.1*float64(env)
		if amp > 0.95 {
			amp = 0.95
		}
	}
	// A triangle wave keeps the peak bounded and the spectrum deterministic.
	tri := 1.0 - 4.0*math.Abs(phase-0.5)
	return tri * amp
}

func dbfs(v float64) float64 {
	if v <= 0 {
		return -math.MaxFloat64
	}
	return 20 * math.Log10(v)
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func hashPCM(pcm []byte) string {
	sum := sha256.Sum256(pcm)
	return hex.EncodeToString(sum[:])
}

// PeakWithin reports whether the measured peak is finite and within [0,1].
func PeakWithin(s Stats) bool {
	return s.Finite && !math.IsNaN(s.Peak) && s.Peak >= 0 && s.Peak <= 1
}
