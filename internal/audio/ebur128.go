package audio

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strconv"

	"rtpregress/internal/clock"
)

// Loudness is the authoritative EBU R128 measurement of delivered PCM.
type Loudness struct {
	IntegratedLUFS float64 // I: integrated loudness
	TruePeakDBTP   float64 // TARGET: maximum true peak
}

// MeasureLoudness runs ffmpeg's ebur128 filter over mono 48 kHz PCM and parses
// the final summary. This is the same measurement engine used industry-wide,
// so the reported LUFS and true peak describe the actual delivered program.
func (c *Converter) MeasureLoudness(ctx context.Context, pcm []byte) (Loudness, error) {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "info",
		"-f", "s16le", "-ar", strconv.Itoa(TargetRate),
		"-ac", strconv.Itoa(TargetChannels), "-i", "pipe:0",
		"-af", "ebur128=peak=true", "-f", "null", "-",
	}
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	cmd.Stdin = bytes.NewReader(pcm)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Loudness{}, clock.New(clock.CodeCanceled, "audio", -1, 0, 0,
				"ebur128 canceled")
		}
		return Loudness{}, clock.New(clock.CodeAudioFFmpeg, "audio", -1, 0, 0,
			"ebur128: %v: %s", err, stderr.String())
	}
	return parseEBUR128(stderr.String())
}

var (
	// Integrated loudness summary line: "I:         -16.0 LUFS". The final
	// occurrence is the summary, not a momentary measurement.
	reIntegrated = regexp.MustCompile(`I:\s+(-?[0-9.]+)\s+LUFS`)
	// True peak summary line; ffmpeg labels it dBFS or dBTP across versions.
	reTruePeak = regexp.MustCompile(`Peak:\s+(-?[0-9.]+)\s+dB(?:FS|TP)`)
)

// parseEBUR128 reads the final (summary, not momentary) I and Peak values.
func parseEBUR128(stderr string) (Loudness, error) {
	intMatches := reIntegrated.FindAllStringSubmatch(stderr, -1)
	tpMatches := reTruePeak.FindAllStringSubmatch(stderr, -1)
	if len(intMatches) == 0 || len(tpMatches) == 0 {
		return Loudness{}, clock.New(clock.CodeAudioFFmpeg, "audio", -1, 0, 0,
			"ebur128 summary not found in ffmpeg output")
	}
	integrated, _ := strconv.ParseFloat(intMatches[len(intMatches)-1][1], 64)
	truePeak, _ := strconv.ParseFloat(tpMatches[len(tpMatches)-1][1], 64)
	return Loudness{IntegratedLUFS: integrated, TruePeakDBTP: truePeak}, nil
}
