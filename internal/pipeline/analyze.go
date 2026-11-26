package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"

	"pwflow/internal/fingerprint"
	"pwflow/internal/media"
	pwrtp "pwflow/internal/rtp"
	"pwflow/internal/silence"
	"pwflow/internal/timebase"
)

const audioWindowNS = 1_000_000_000

type audioAnalysis struct {
	pcm        []byte
	durationNS int64
	silence    silence.Report
	windows    []AudioWindowInfo
}

func analyzeAudio(ctx context.Context, path string, sampleRate int, gaps []pwrtp.Gap) (*audioAnalysis, error) {
	pcm, err := media.DecodeS16Mono(ctx, path, sampleRate)
	if err != nil {
		return nil, err
	}
	exclusions := make([]timebase.Interval, 0, len(gaps))
	for _, g := range gaps {
		if g.Mapped && g.EndNS > g.StartNS {
			exclusions = append(exclusions, timebase.Interval{Lo: g.StartNS, Hi: g.EndNS})
		}
	}
	rep := silence.DetectS16LE(pcm, silence.Options{
		SampleRate: sampleRate,
		Exclusions: exclusions,
	})
	samples := len(pcm) / 2
	durationNS := int64(samples) * timebase.Seconds(1) / int64(sampleRate)
	windows := hashAudioWindows(pcm, sampleRate, durationNS)
	return &audioAnalysis{pcm: pcm, durationNS: durationNS, silence: rep, windows: windows}, nil
}

func hashAudioWindows(pcm []byte, sampleRate int, durationNS int64) []AudioWindowInfo {
	var out []AudioWindowInfo
	for startNS := int64(0); startNS < durationNS; startNS += audioWindowNS {
		endNS := startNS + audioWindowNS
		if endNS > durationNS {
			endNS = durationNS
		}
		startSample := int(startNS * int64(sampleRate) / timebase.Seconds(1))
		endSample := int(endNS * int64(sampleRate) / timebase.Seconds(1))
		startByte := startSample * 2
		endByte := endSample * 2
		if startByte < 0 || startByte > len(pcm) {
			continue
		}
		if endByte > len(pcm) || endByte < startByte {
			endByte = len(pcm)
		}
		chunk := pcm[startByte:endByte]
		sha := fingerprint.AudioWindow(startNS, endNS, sampleRate, chunk)
		out = append(out, AudioWindowInfo{
			Interval: timebase.Interval{Lo: startNS, Hi: endNS},
			SHA256:   sha,
		})
	}
	return out
}

func analyzeVideoIntoSegments(
	ctx context.Context,
	path string,
	width, height, fps int,
	segments []*SegmentResult,
	registry *fingerprint.Registry,
) (float64, int, error) {
	var globalLumaSum float64
	var globalFrames int
	err := media.DecodeRGB(ctx, path, width, height, fps, func(index int, ptsNS int64, rgb []byte) error {
		segIdx := segmentIndexAt(segments, ptsNS)
		if segIdx < 0 {
			return nil
		}
		luma := frameLuma(rgb)
		sha := fingerprint.Frame(ptsNS, width, height, "rgb24", rgb)
		pixel := fingerprint.Pixel(width, height, rgb)
		frame := FrameInfo{
			Index: len(segments[segIdx].Frames), GlobalIndex: index, PTSNS: ptsNS,
			SHA256: sha, PixelSHA256: pixel, LumaMean: round2(luma),
		}
		segments[segIdx].Frames = append(segments[segIdx].Frames, frame)
		segments[segIdx].FrameCount = len(segments[segIdx].Frames)
		globalLumaSum += luma
		globalFrames++
		registry.Add(pixel, fingerprint.Locator{
			Segment: segIdx, Kind: "pixel", Index: int64(frame.Index), PTSNS: ptsNS,
		})
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	for _, seg := range segments {
		var sum float64
		for _, f := range seg.Frames {
			sum += f.LumaMean
		}
		if len(seg.Frames) > 0 {
			seg.LumaMean = round2(sum / float64(len(seg.Frames)))
			seg.FramesSHA256 = hashFrameList(seg.Frames)
		}
	}
	if globalFrames == 0 {
		return 0, 0, nil
	}
	return round2(globalLumaSum / float64(globalFrames)), globalFrames, nil
}

func segmentIndexAt(segments []*SegmentResult, ptsNS int64) int {
	for i, s := range segments {
		if s.Cut.Actual.Contains(ptsNS) {
			return i
		}
	}
	return -1
}

func frameLuma(rgb []byte) float64 {
	var sum uint64
	pixels := len(rgb) / 3
	for i := 0; i+2 < len(rgb); i += 3 {
		r := uint64(rgb[i])
		g := uint64(rgb[i+1])
		b := uint64(rgb[i+2])
		sum += (299*r + 587*g + 114*b) / 1000
	}
	if pixels == 0 {
		return 0
	}
	return float64(sum) / float64(pixels)
}

func hashFrameList(frames []FrameInfo) string {
	h := sha256.New()
	for _, f := range frames {
		h.Write([]byte(f.SHA256))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
