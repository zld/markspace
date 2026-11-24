package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"time"

	"mediatool/internal/fault"
	"mediatool/internal/fixture"
	"mediatool/internal/index"
	"mediatool/internal/segment"
	"mediatool/internal/stream"
	"mediatool/internal/video"
)

// demoResult is the machine-readable summary printed as key=value lines.
type demoResult struct {
	tracks          int
	representations int
	lossPackets     int
	corruptions     int
	damageOffset    int64
	offsetError     int64
	resumeMs        int64
	manifestSegs    int
	alignmentSkew   int64
	outputSHA       string
	workDir         string
	manifestPath    string
}

// demo runs generate -> scan -> index -> align -> segment -> interrupt ->
// resume -> verify, and returns the summary.
func demo(seed int64, tracks, bitrates, injectLoss, injectTrunc, interruptAfter int, dir string) (demoResult, error) {
	opts := fixture.DemoOptions(seed, tracks, bitrates, injectLoss, injectTrunc)
	cap, err := fixture.Generate(opts)
	if err != nil {
		return demoResult{}, err
	}

	scanRes, err := buildIndex(cap, opts)
	if err != nil {
		return demoResult{}, err
	}
	ix := scanRes.Index

	// Persist the index and prove it is bound to this exact input.
	indexPath := filepath.Join(dir, "index.json")
	if err := ix.Save(indexPath); err != nil {
		return demoResult{}, err
	}
	if _, err := index.Load(indexPath, ix.InputDigest); err != nil {
		return demoResult{}, err
	}

	reps, err := buildRepresentations(opts, bitrates)
	if err != nil {
		return demoResult{}, err
	}

	in := stream.Input{
		Digest:           ix.InputDigest,
		InputBytes:       int64(len(cap.Bytes)),
		Index:            ix,
		Reader:           bytes.NewReader(cap.Bytes),
		Representations:  reps,
		TruncatedOffsets: cap.TruncationOffsets,
	}

	checkpointPath := filepath.Join(dir, "checkpoint.json")
	cfg := stream.Config{
		Window:            256,
		MaxSegmentBytes:   1 << 20,
		CheckpointPath:    checkpointPath,
		OutputDir:         filepath.Join(dir, "segments"),
		StopAfterSegments: interruptAfter,
		Segmenter:         segment.Config{DurationMs: opts.SegmentMs, KeyframeSearchMs: opts.SegmentMs},
	}

	first, err := stream.Execute(in, cfg)
	if err != nil {
		return demoResult{}, err
	}

	final := first
	var resumeMs int64
	if interruptAfter > 0 && first.Stopped {
		resumeCfg := cfg
		resumeCfg.ResumeFrom = checkpointPath
		resumeCfg.StopAfterSegments = 0
		start := time.Now()
		second, err := stream.Execute(in, resumeCfg)
		if err != nil {
			return demoResult{}, err
		}
		resumeMs = time.Since(start).Milliseconds()
		final = second
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	if err := final.Manifest.Save(manifestPath); err != nil {
		return demoResult{}, err
	}
	encoded, err := final.Manifest.Encode()
	if err != nil {
		return demoResult{}, err
	}

	res := demoResult{
		tracks:          len(opts.Tracks),
		representations: len(final.Manifest.Representations),
		lossPackets:     cap.LossInjected,
		corruptions:     cap.Truncations,
		damageOffset:    reportedDamageOffset(cap, scanRes),
		offsetError:     offsetError(cap, scanRes),
		resumeMs:        resumeMs,
		manifestSegs:    len(final.Manifest.Segments),
		alignmentSkew:   final.Manifest.AlignmentSkewMs,
		outputSHA:       index.DigestBytes(encoded),
	}
	return res, nil
}

// reportedDamageOffset is the byte offset the scanner reported for the first
// truncation. The fixture's ground truth is used only by offsetError.
func reportedDamageOffset(cap *fixture.Capture, scanRes *index.ScanResult) int64 {
	if f := firstKind(scanRes, fault.KindTruncated); f != nil {
		return f.Offset
	}
	if len(cap.TruncationOffsets) > 0 {
		return cap.TruncationOffsets[0]
	}
	if f := firstDamage(scanRes); f != nil {
		return f.Offset
	}
	return 0
}

// offsetError is the distance between the scanner's reported first suspicious
// byte and the fixture's ground-truth offset. The contract allows at most one
// RTP header (12 bytes).
func offsetError(cap *fixture.Capture, scanRes *index.ScanResult) int64 {
	if len(cap.TruncationOffsets) == 0 {
		return 0
	}
	f := firstKind(scanRes, fault.KindTruncated)
	if f == nil {
		return 0
	}
	d := f.Offset - cap.TruncationOffsets[0]
	if d < 0 {
		d = -d
	}
	return d
}

func firstKind(scanRes *index.ScanResult, kind fault.Kind) *fault.Fault {
	var best *fault.Fault
	for _, f := range scanRes.Faults {
		if f.Kind != kind {
			continue
		}
		if best == nil || f.Offset < best.Offset {
			best = f
		}
	}
	return best
}

func firstDamage(scanRes *index.ScanResult) *fault.Fault {
	var best *fault.Fault
	for _, f := range scanRes.Faults {
		if f.Severity < fault.SeverityDamaged || f.Offset <= 0 {
			continue
		}
		if best == nil || f.Offset < best.Offset {
			best = f
		}
	}
	return best
}

// buildIndex scans the capture with track identities and normalized metadata.
func buildIndex(cap *fixture.Capture, opts fixture.Options) (*index.ScanResult, error) {
	tracks := make(map[uint32]index.TrackInfo, len(opts.Tracks))
	kinds := make(map[uint32]string, len(opts.Tracks))
	for _, spec := range opts.Tracks {
		tracks[spec.SSRC] = index.TrackInfo{TrackID: spec.TrackID, ClockRate: spec.ClockRate}
		kinds[spec.TrackID] = spec.Kind
	}
	scanRes, err := index.ScanReader(bytes.NewReader(cap.Bytes), index.ScanConfig{
		Tracks:     tracks,
		Kinds:      kinds,
		Window:     256,
		IsKeyframe: index.IsVideoKeyframe,
		Digest:     index.DigestBytes(cap.Bytes),
		InputBytes: int64(len(cap.Bytes)),
	})
	if err != nil {
		return nil, err
	}
	// Attach normalized audio and video metadata to the index. A color
	// conflict is a warning the run records; it does not abort.
	for _, spec := range opts.Tracks {
		if spec.Kind == "audio" {
			info, err := index.NormalizeAudio(spec.TrackID, spec.Layout, spec.SampleRate, "stereo", 48000, 0)
			if err != nil {
				return nil, err
			}
			scanRes.Index.SetAudio(spec.TrackID, *info)
			continue
		}
		vi, verr := index.NormalizeVideo(spec.TrackID, spec.Width, spec.Height, spec.ColorCandidates)
		if verr != nil {
			if _, ok := fault.As(verr); !ok {
				return nil, verr
			}
		}
		scanRes.Index.SetVideo(spec.TrackID, vi)
	}
	return scanRes, nil
}

// buildRepresentations expands the track set into a bitrate ladder. Every rung
// packages all tracks, so representation count equals the ladder depth.
func buildRepresentations(opts fixture.Options, bitrates int) ([]stream.RepresentationSpec, error) {
	reps := make([]stream.RepresentationSpec, 0, bitrates)
	for b := 1; b <= bitrates; b++ {
		bitrate := b * 1_000_000
		rep := stream.RepresentationSpec{
			ID:      fmt.Sprintf("r%d", bitrate/1_000_000),
			Bitrate: bitrate,
			Codec:   "avc1+mp4a",
		}
		for _, spec := range opts.Tracks {
			t := stream.TrackSpec{TrackID: spec.TrackID, Kind: spec.Kind}
			if spec.Kind == "audio" {
				info, err := index.NormalizeAudio(spec.TrackID, spec.Layout, spec.SampleRate, "stereo", 48000, 0)
				if err != nil {
					return nil, err
				}
				t.Audio = info
			} else {
				vi, err := index.NormalizeVideo(spec.TrackID, spec.Width, spec.Height, spec.ColorCandidates)
				if err != nil {
					if _, ok := fault.As(err); !ok {
						return nil, err
					}
				}
				t.Video = &vi
				dec, _ := video.Normalize(spec.ColorCandidates)
				t.ColorRecord = dec.Record()
			}
			rep.Tracks = append(rep.Tracks, t)
		}
		reps = append(reps, rep)
	}
	return reps, nil
}
