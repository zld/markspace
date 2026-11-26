package validate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"path/filepath"

	"pwflow/internal/ebml"
	"pwflow/internal/fingerprint"
	"pwflow/internal/media"
	"pwflow/internal/pipeline"
)

func newSHA() hash.Hash { return sha256.New() }

func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

func checkAlignment(rep *Report, m *pipeline.Manifest, ebmlRep *ebml.Report) {
	var keys []int64
	videoTrack := uint64(1)
	for _, t := range ebmlRep.Tracks {
		if t.Type == 1 {
			videoTrack = t.Number
		}
	}
	for _, b := range ebmlRep.Blocks {
		if b.TrackNumber == videoTrack && b.Keyframe {
			keys = append(keys, b.PTSNS)
		}
	}
	var max int64
	for _, s := range m.Segments {
		startDist := nearestDistance(s.Cut.Requested.Lo, keys)
		endDist := nearestDistance(s.Cut.Requested.Hi, keys)
		if startDist != s.Cut.StartErrorNS || endDist != s.Cut.EndErrorNS {
			rep.fail("cut-errors", fmt.Sprintf("segment %d manifest=(%d,%d) recomputed=(%d,%d)",
				s.Index, s.Cut.StartErrorNS, s.Cut.EndErrorNS, startDist, endDist))
		}
		if s.Cut.MaxErrorNS > max {
			max = s.Cut.MaxErrorNS
		}
	}
	if max != m.Alignment.CutMaxErrorNS || max != m.Alignment.MaxErrorNS {
		rep.fail("alignment-max", fmt.Sprintf("manifest=%d cut=%d recomputed=%d",
			m.Alignment.MaxErrorNS, m.Alignment.CutMaxErrorNS, max))
	} else {
		rep.pass("alignment-recomputed", fmt.Sprintf("max_error_ns=%d keyframes=%d", max, len(keys)))
	}
}

func nearestDistance(target int64, keys []int64) int64 {
	if len(keys) == 0 {
		return math.MaxInt64
	}
	best := absInt64(keys[0] - target)
	for _, k := range keys[1:] {
		if d := absInt64(k - target); d < best {
			best = d
		}
	}
	return best
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func checkVideo(ctx context.Context, rep *Report, m *pipeline.Manifest, path string, w, h int) {
	expected := map[int]pipeline.FrameInfo{}
	segOf := map[int]int{}
	for _, s := range m.Segments {
		for _, f := range s.Frames {
			expected[f.GlobalIndex] = f
			segOf[f.GlobalIndex] = s.Index
		}
	}
	if len(expected) == 0 {
		rep.fail("video-frames", "manifest contains no frame hashes")
		return
	}
	var globalSum float64
	var globalN int
	segSums := map[int]float64{}
	segN := map[int]int{}
	err := media.DecodeRGB(ctx, path, w, h, 30, func(index int, ptsNS int64, rgb []byte) error {
		want, ok := expected[index]
		if !ok {
			return nil
		}
		if want.PTSNS != ptsNS {
			rep.fail("frame-pts", fmt.Sprintf("frame %d pts=%d want=%d", index, ptsNS, want.PTSNS))
		}
		luma := round2(frameLuma(rgb))
		if luma != want.LumaMean {
			rep.fail("pixel-luma", fmt.Sprintf("frame %d luma=%.2f want=%.2f", index, luma, want.LumaMean))
		}
		if got := fingerprint.Frame(ptsNS, w, h, "rgb24", rgb); got != want.SHA256 {
			rep.fail("frame-hash", fmt.Sprintf("frame %d hash mismatch", index))
		}
		if got := fingerprint.Pixel(w, h, rgb); got != want.PixelSHA256 {
			rep.fail("pixel-hash", fmt.Sprintf("frame %d pixel hash mismatch", index))
		}
		globalSum += frameLuma(rgb)
		globalN++
		si := segOf[index]
		segSums[si] += frameLuma(rgb)
		segN[si]++
		return nil
	})
	if err != nil {
		rep.fail("video-decode", err.Error())
		return
	}
	if globalN != len(expected) {
		rep.fail("video-frame-count", fmt.Sprintf("verified=%d manifest=%d", globalN, len(expected)))
		return
	}
	globalLuma := round2(globalSum / float64(globalN))
	if math.Abs(globalLuma-m.LumaMean) > 0.01 {
		rep.fail("luma-mean", fmt.Sprintf("%.2f != manifest %.2f", globalLuma, m.LumaMean))
	}
	for _, s := range m.Segments {
		if segN[s.Index] == 0 {
			continue
		}
		got := round2(segSums[s.Index] / float64(segN[s.Index]))
		if math.Abs(got-s.LumaMean) > 0.01 {
			rep.fail("segment-luma", fmt.Sprintf("segment %d %.2f != %.2f", s.Index, got, s.LumaMean))
		}
	}
	rep.pass("video-frames", fmt.Sprintf("verified %d frame hashes and pixel means", globalN))
}

func checkAudio(ctx context.Context, rep *Report, m *pipeline.Manifest) {
	const rate = 48000
	pcm, err := media.DecodeS16Mono(ctx, m.Inputs.MediaPath, rate)
	if err != nil {
		rep.fail("audio-decode", err.Error())
		return
	}
	samples := media.Int16Samples(pcm)
	peak := 0
	for _, v := range samples {
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	peakDB := -96.0
	if peak > 0 {
		peakDB = round2(20 * math.Log10(float64(peak)/32768.0))
	}
	if m.LoudnessPeakDB == nil || math.Abs(*m.LoudnessPeakDB-peakDB) > 0.05 {
		want := -999.0
		if m.LoudnessPeakDB != nil {
			want = *m.LoudnessPeakDB
		}
		rep.fail("loudness-peak", fmt.Sprintf("recomputed=%.2f manifest=%.2f", peakDB, want))
	} else {
		rep.pass("loudness-peak", fmt.Sprintf("%.2f dBFS", peakDB))
	}
	for _, w := range m.AudioWindows {
		start := int(w.Lo * int64(rate) / 1_000_000_000 * 2)
		end := int(w.Hi * int64(rate) / 1_000_000_000 * 2)
		if start < 0 || start > len(pcm) || end < start || end > len(pcm) {
			rep.fail("audio-window-bounds", fmt.Sprintf("%+v outside %d PCM bytes", w.Interval, len(pcm)))
			continue
		}
		if got := fingerprint.AudioWindow(w.Lo, w.Hi, rate, pcm[start:end]); got != w.SHA256 {
			rep.fail("audio-window-hash", fmt.Sprintf("window %+v mismatch", w.Interval))
		}
	}
	rep.pass("audio-window-hashes", fmt.Sprintf("verified %d windows", len(m.AudioWindows)))
}

func checkSegmentPixels(ctx context.Context, rep *Report, m *pipeline.Manifest, manifestDir string, w, h int) {
	for _, s := range m.Segments {
		path := filepath.Join(manifestDir, s.File)
		seen := 0
		err := media.DecodeRGB(ctx, path, w, h, 30, func(index int, _ int64, rgb []byte) error {
			if index >= len(s.Frames) {
				return fmt.Errorf("segment contains extra frame %d", index)
			}
			want := s.Frames[index]
			if got := fingerprint.Pixel(w, h, rgb); got != want.PixelSHA256 {
				return fmt.Errorf("segment %d frame %d pixel hash mismatch", s.Index, index)
			}
			if got := round2(frameLuma(rgb)); math.Abs(got-want.LumaMean) > 0.01 {
				return fmt.Errorf("segment %d frame %d luma %.2f != %.2f", s.Index, index, got, want.LumaMean)
			}
			seen++
			return nil
		})
		if err != nil {
			rep.fail("segment-pixels:"+s.File, err.Error())
			continue
		}
		if seen != len(s.Frames) {
			rep.fail("segment-frame-count:"+s.File, fmt.Sprintf("decoded=%d manifest=%d", seen, len(s.Frames)))
			continue
		}
		rep.pass("segment-pixels:"+s.File, fmt.Sprintf("verified %d decoded output frames", seen))
	}
}

func frameLuma(rgb []byte) float64 {
	var sum uint64
	pixels := len(rgb) / 3
	for i := 0; i+2 < len(rgb); i += 3 {
		r, g, b := uint64(rgb[i]), uint64(rgb[i+1]), uint64(rgb[i+2])
		sum += (299*r + 587*g + 114*b) / 1000
	}
	if pixels == 0 {
		return 0
	}
	return float64(sum) / float64(pixels)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
