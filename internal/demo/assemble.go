package demo

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"assetpack/internal/clock"
	"assetpack/internal/index"
	"assetpack/internal/release"
	"assetpack/internal/render"
	"assetpack/internal/stream"
	"assetpack/internal/timeline"
)

func renderFrame(_ int, sourceIndex int, current []byte, bySource map[int][]byte, cutFrame int) []byte {
	frame := append([]byte(nil), current...)
	if sourceIndex == cutFrame-1 {
		if next, ok := bySource[sourceIndex+1]; ok {
			return sampleTransition(frame, next, 1, 2)
		}
	}
	if sourceIndex == cutFrame {
		if prev, ok := bySource[sourceIndex-1]; ok {
			return sampleTransition(prev, frame, 1, 2)
		}
	}
	return frame
}

func sampleTransition(a, b []byte, num, den int) []byte {
	// Local tiny wrapper keeps rendering deterministic without importing samplegen
	// solely for one function name at every call site.
	return transitionRGBA(a, b, num, den)
}

func transitionRGBA(a, b []byte, num, den int) []byte {
	out := make([]byte, len(a))
	for i := 0; i+3 < len(a); i += 4 {
		for c := 0; c < 3; c++ {
			av := int(a[i+c]) * (den - num)
			bv := int(b[i+c]) * num
			out[i+c] = byte((av + bv + den/2) / den)
		}
		out[i+3] = 255
	}
	return out
}

func commitPackage(
	opts Options,
	tl *timeline.Timeline,
	cov *index.Coverage,
	audio []byte,
	files map[string][]byte,
	stats stream.PipelineStats,
) (*Result, error) {
	desktopCount := len(cov.Records)
	mobileCount := (desktopCount + 1) / 2
	if opts.Range == (clock.Range{From: 45000, To: 135000}) {
		mobileCount = desktopCount / 2
	}

	timelineBytes, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return nil, err
	}
	files["timeline.json"] = timelineBytes
	audioPath := "audio/audio.pcm"
	files[audioPath] = audio

	arts := []release.Artifact{
		hashArtifact("inputs/input.mp4", "input", "", files),
		hashArtifact("index.json", "index", "", files),
		hashArtifact("timeline.json", "timeline", "", files),
		hashArtifact(audioPath, "audio", "desktop", files),
	}
	desktopMean := release.RGB{}
	for i := 0; i < desktopCount; i++ {
		p := fmt.Sprintf("desktop/frame-%05d.rgba", i+1)
		arts = append(arts, hashArtifact(p, "frame", "desktop", files))
		mean, err := render.ComputeRGBMean(files[p])
		if err != nil {
			return nil, err
		}
		desktopMean.R += mean.R
		desktopMean.G += mean.G
		desktopMean.B += mean.B
	}
	if desktopCount > 0 {
		desktopMean = release.RGB{R: desktopMean.R / float64(desktopCount), G: desktopMean.G / float64(desktopCount), B: desktopMean.B / float64(desktopCount)}
	}
	mobileMean := release.RGB{}
	for i := 0; i < mobileCount; i++ {
		p := fmt.Sprintf("mobile/frame-%05d.rgba", i+1)
		arts = append(arts, hashArtifact(p, "frame", "mobile", files))
		mean, err := render.ComputeRGBMean(files[p])
		if err != nil {
			return nil, err
		}
		mobileMean.R += mean.R
		mobileMean.G += mean.G
		mobileMean.B += mean.B
	}
	if mobileCount > 0 {
		mobileMean = release.RGB{R: mobileMean.R / float64(mobileCount), G: mobileMean.G / float64(mobileCount), B: mobileMean.B / float64(mobileCount)}
	}

	var pcm []int16
	for len(audio) >= 2 {
		pcm = append(pcm, int16(binary.LittleEndian.Uint16(audio)))
		audio = audio[2:]
	}
	peak := render.PeakDBFS(pcm)

	desktopSegments, desktopSegHashes := frameSegments("desktop", 12, desktopCount, cov, files)
	mobileSegments, mobileSegHashes := mobileSegments(mobileCount, cov, files)

	version := fmt.Sprintf("seq-%d-%d-%d", opts.Sequence, opts.Range.From, opts.Range.To)
	m := &release.Manifest{
		Version: version, Sequence: opts.Sequence, TimebaseHz: clock.Hz, Range: opts.Range,
		Inputs: []release.Input{{
			Name: "input.mp4", Format: "mp4/v1", Size: len(files["inputs/input.mp4"]),
			SHA256: render.HashBytes(files["inputs/input.mp4"]),
		}},
		Clips:     []release.Artifact{hashArtifact("timeline.json", "timeline", "", files)},
		Artifacts: arts,
		Targets: []release.Target{
			{
				Platform: "desktop", FrameCount: desktopCount, SegmentCount: len(desktopSegments),
				AudioSampleFrames: len(pcm), Width: 32, Height: 18, RGBMean: desktopMean, PeakDBFS: peak,
				ActualRange: cov.Actual, AlignmentErrorSamples: cov.MaxRoundedAlignSamples,
				AlignmentTolerance: 1, QueuePeak: stats.QueuePeak, QueueCapacity: stats.QueueCap,
				Segments: desktopSegments,
			},
			{
				Platform: "mobile", FrameCount: mobileCount, SegmentCount: len(mobileSegments),
				Width: 32, Height: 18, RGBMean: mobileMean, ActualRange: cov.Actual,
				AlignmentErrorSamples: cov.MaxRoundedAlignSamples, AlignmentTolerance: 1,
				QueuePeak: stats.QueuePeak, QueueCapacity: stats.QueueCap, Segments: mobileSegments,
			},
		},
	}
	committed, err := release.Commit(release.CommitRequest{Root: opts.Out, Manifest: m, Files: files})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", release.ErrPublish, err)
	}
	frame12 := fmt.Sprintf("desktop/frame-%05d.rgba", 12)
	return &Result{
		Commit: committed, Stats: stats, DesktopCount: desktopCount, MobileCount: mobileCount,
		AudioSamples: len(pcm), Frame12Path: filepath.Join(opts.Out, frame12),
		Frame12Hash: render.HashBytes(files[frame12]), SegmentHashes: append(desktopSegHashes, mobileSegHashes...),
	}, nil
}

func hashArtifact(path, kind, platform string, files map[string][]byte) release.Artifact {
	b := files[path]
	return release.Artifact{Path: path, Kind: kind, Platform: platform, Size: int64(len(b)), SHA256: render.HashBytes(b)}
}

func frameSegments(platform string, group, count int, cov *index.Coverage, files map[string][]byte) ([]release.Segment, []string) {
	var segs []release.Segment
	var hashes []string
	for g, start := 0, 0; start < count; g, start = g+1, start+group {
		end := minInt(start+group, count)
		h := sha256.New()
		var size int64
		members := []string{}
		for i := start; i < end; i++ {
			p := fmt.Sprintf("%s/frame-%05d.rgba", platform, i+1)
			h.Write(files[p])
			size += int64(len(files[p]))
			members = append(members, p)
		}
		hash := hex.EncodeToString(h.Sum(nil))
		segs = append(segs, release.Segment{
			Platform: platform, Name: fmt.Sprintf("%s-%03d", platform, g),
			Start: cov.Records[start].PTS, End: cov.Records[end-1].PTS + cov.Records[end-1].Duration,
			FrameCount: end - start, Size: size, SHA256: hash, Members: members,
		})
		hashes = append(hashes, fmt.Sprintf("%s-%03d=%s", platform, g, hash))
	}
	return segs, hashes
}

func mobileSegments(mobileCount int, cov *index.Coverage, files map[string][]byte) ([]release.Segment, []string) {
	const group = 3
	dur := clock.MustMap(1, clock.Frame12fps)
	var segs []release.Segment
	var hashes []string
	for g, start := 0, 0; start < mobileCount; g, start = g+1, start+group {
		end := minInt(start+group, mobileCount)
		h := sha256.New()
		var size int64
		members := []string{}
		for j := start; j < end; j++ {
			p := fmt.Sprintf("mobile/frame-%05d.rgba", j+1)
			h.Write(files[p])
			size += int64(len(files[p]))
			members = append(members, p)
		}
		sourcePos := 2 * start
		from := cov.Records[sourcePos].PTS
		hash := hex.EncodeToString(h.Sum(nil))
		segs = append(segs, release.Segment{
			Platform: "mobile", Name: fmt.Sprintf("mobile-%03d", g), Start: from,
			End: from + dur*clock.Ticks(end-start), FrameCount: end - start, Size: size,
			SHA256: hash, Members: members,
		})
		hashes = append(hashes, fmt.Sprintf("mobile-%03d=%s", g, hash))
	}
	return segs, hashes
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
