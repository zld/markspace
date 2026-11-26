package process

import (
	"math"
	"strconv"

	"mediaprep/internal/model"
	"mediaprep/internal/source"
)

type SilenceConfig struct {
	ThresholdDB  float64        `json:"threshold_db"`
	MinDuration  model.Rational `json:"min_duration_seconds"`
	WindowLength int            `json:"window_frames"`
	Clock        model.Clock    `json:"clock"`
}

func DefaultSilenceConfig() SilenceConfig {
	return SilenceConfig{
		ThresholdDB:  -60,
		MinDuration:  model.Rational{Numerator: 1, Denominator: 4}, // 250 ms
		WindowLength: 480,                                          // 10 ms at 48 kHz
		Clock:        DeliveryClock,
	}
}

type SilenceInterval struct {
	Track         string         `json:"track"`
	Interval      model.Interval `json:"interval"`
	StartFrame    int64          `json:"start_frame"`
	EndFrame      int64          `json:"end_frame"`
	DurationMS    float64        `json:"duration_ms"`
	RMSLinear     float64        `json:"rms_linear"`
	RMSDB         float64        `json:"rms_db"`
	RMSDBText     string         `json:"rms_db_text"`
	PeakAmplitude float64        `json:"peak_amplitude"`
	PeakDBFS      float64        `json:"peak_dbfs"`
	WindowCount   int            `json:"window_count"`
}

// DetectSilence implements the fixed rule: RMS <= -60 dBFS for at least 250 ms.
// It operates only on signed little-endian 16-bit PCM and reports exact tick
// endpoints on the shared rational delivery clock.
func DetectSilence(track *source.Track, cfg SilenceConfig) ([]SilenceInterval, error) {
	if track.PCM == nil || track.Codec != "pcm_s16le" {
		return nil, model.NewError(model.CodeUnsupportedCodec, model.PhaseProcess, trackLabel(track), -1,
			"silence detection requires pcm_s16le; track codec %q is not decodable", track.Codec)
	}
	if cfg.ThresholdDB == 0 {
		cfg = DefaultSilenceConfig()
	}
	if cfg.WindowLength <= 0 {
		cfg.WindowLength = 480
	}
	channels := track.PCM.Channels
	if channels <= 0 {
		channels = 1
	}

	frames, err := collectPCMFrames(track, channels)
	if err != nil {
		return nil, err
	}
	minFrames := track.Clock.TicksFromSeconds(cfg.MinDuration)
	if minFrames <= 0 {
		minFrames = int64(track.PCM.SampleRate / 4)
	}

	type run struct{ start, end int64 }
	var runs []run
	runStart := int64(-1)
	totalFrames := int64(len(frames))
	for start := int64(0); start < totalFrames; start += int64(cfg.WindowLength) {
		end := start + int64(cfg.WindowLength)
		if end > totalFrames {
			end = totalFrames
		}
		rms, _ := rmsPeak(frames[start:end])
		quiet := dbFS(rms) <= cfg.ThresholdDB
		if quiet && runStart < 0 {
			runStart = start
		}
		if !quiet && runStart >= 0 {
			runs = append(runs, run{runStart, start})
			runStart = -1
		}
	}
	if runStart >= 0 {
		runs = append(runs, run{runStart, totalFrames})
	}

	var out []SilenceInterval
	for _, r := range runs {
		if r.end-r.start < minFrames {
			continue
		}
		rms, peak := rmsPeak(frames[r.start:r.end])
		rmsDB := dbFS(rms)
		peakDB := dbFS(peak)
		startShared := model.ConvertTicks(r.start, track.Clock, cfg.Clock)
		endShared := model.ConvertTicks(r.end, track.Clock, cfg.Clock)
		out = append(out, SilenceInterval{
			Track: track.Label,
			Interval: model.Interval{
				Start: startShared, End: endShared, Scale: cfg.Clock.TickDuration,
			},
			StartFrame: r.start, EndFrame: r.end,
			DurationMS: ticksToMS(r.end-r.start, track.Clock),
			RMSLinear:  rms, RMSDB: finiteOr(rmsDB, -100), RMSDBText: dbText(rmsDB),
			PeakAmplitude: peak, PeakDBFS: finiteOr(peakDB, -100),
			WindowCount: int((r.end - r.start + int64(cfg.WindowLength) - 1) / int64(cfg.WindowLength)),
		})
	}
	return out, nil
}

func collectPCMFrames(track *source.Track, channels int) ([]float64, error) {
	var samples []float64
	for _, s := range track.Samples {
		if len(s.Payload)%2 != 0 {
			return nil, model.NewError(model.CodeTruncated, model.PhaseProcess, track.Label, s.Offset,
				"PCM sample has odd byte length")
		}
		for i := 0; i+1 < len(s.Payload); i += 2 {
			v := int16(uint16(s.Payload[i]) | uint16(s.Payload[i+1])<<8)
			samples = append(samples, float64(v)/32768.0)
		}
	}
	_ = channels // RMS is computed over every interleaved channel sample.
	return samples, nil
}

func rmsPeak(xs []float64) (rms, peak float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x * x
		if a := math.Abs(x); a > peak {
			peak = a
		}
	}
	return math.Sqrt(sum / float64(len(xs))), peak
}

func dbFS(v float64) float64 {
	if v <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(v)
}

func dbText(v float64) string {
	if math.IsInf(v, -1) {
		return "-inf dBFS"
	}
	return formatFloat(v, 2) + " dBFS"
}

func finiteOr(v, fallback float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return fallback
	}
	return v
}

func formatFloat(v float64, digits int) string {
	return strconv.FormatFloat(v, 'f', digits, 64)
}

func trackLabel(t *source.Track) string {
	if t == nil {
		return ""
	}
	return t.Label
}
