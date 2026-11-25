// Package verify recomputes terminal-review metrics from artifacts.
package verify

import (
	"encoding/json"
	"math"
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/manifest"
	"mediacore/internal/maptrack"
	"mediacore/internal/scan"
)

// Request configures lossless verification.
type Request struct {
	InputPath     string
	OutputPath    string
	CachePath     string
	TargetBitrate int64
	Mappings      []scan.TrackMapping
	Params        scan.Params
}

// Result contains metrics and rescanned indexes.
type Result struct {
	Metrics     manifest.Metrics
	InputIndex  *scan.Index
	OutputIndex *scan.Index
}

// VerifyMP4 rescans input/output and compares compressed sample bytes.
func VerifyMP4(req Request) (*Result, error) {
	params := req.Params
	if params.MaxEntries <= 0 {
		params = scan.DefaultParams()
	}
	in, err := scan.BuildIndex(req.InputPath, params)
	if err != nil {
		return nil, err
	}
	out, err := scan.BuildIndex(req.OutputPath, params)
	if err != nil {
		return nil, err
	}
	mappings := req.Mappings
	if len(mappings) == 0 {
		mappings, err = maptrack.Map(in)
		if err != nil {
			return nil, err
		}
	}

	inRaw, err := os.ReadFile(req.InputPath)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), req.InputPath, -1)
	}
	outRaw, err := os.ReadFile(req.OutputPath)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), req.OutputPath, -1)
	}

	inSamples := groupSamples(in.Samples)
	outSamples := groupSamples(out.Samples)
	outTrackByID := map[uint32]scan.Track{}
	for _, t := range out.Tracks {
		outTrackByID[t.ID] = t
	}

	var audioDiffs int64
	videoExact := true
	comparedVideo := false
	for _, mp := range mappings {
		expected := inSamples[mp.SourceTrackID]
		actual := outSamples[mp.OutputTrackID]
		if len(actual) != len(expected) {
			return nil, fault.New(fault.Verification, "sample count differs for mapped track")
		}
		for i := range expected {
			ab := sliceSample(inRaw, expected[i])
			bb := sliceSample(outRaw, actual[i])
			if string(ab) != string(bb) {
				if mp.Kind == "audio" {
					audioDiffs += countAudioSampleDiffs(ab, bb)
				} else {
					videoExact = false
				}
			}
		}
		if mp.Kind == "video" && len(expected) > 0 {
			comparedVideo = true
		}
	}
	if !videoExact {
		return nil, fault.New(fault.Verification, "video payload is not bit-exact")
	}
	if audioDiffs != 0 {
		return nil, fault.New(fault.Verification, "audio payload differs")
	}

	durationNS := maxDuration(out.Tracks)
	if durationNS <= 0 {
		return nil, fault.New(fault.Verification, "output duration is zero")
	}
	st, _ := os.Stat(req.OutputPath)
	actualBitrate := float64(st.Size()) * 8.0 / (float64(durationNS) / float64(clock.NSPerSecond))
	target := req.TargetBitrate
	if target <= 0 {
		target = params.TargetBitrate
	}
	deltaPct := math.Abs(actualBitrate-float64(target)) / float64(target) * 100
	alignmentMS, err := alignment(out)
	if err != nil {
		return nil, err
	}

	psnr := "+Inf/exact"
	psnrValue := math.Inf(1)
	if !comparedVideo {
		psnr = "n/a"
		psnrValue = math.NaN()
	}
	m := manifest.Metrics{
		TargetBitrate:       target,
		ActualBitrate:       round2(actualBitrate),
		BitrateDeltaPct:     round4(deltaPct),
		VideoPSNR:           psnr,
		VideoPSNRValue:      psnrValue,
		AudioDiffSamples:    audioDiffs,
		ManifestAlignmentMS: round4(alignmentMS),
	}
	if m.BitrateDeltaPct > 1.0 {
		return nil, fault.New(fault.Verification, "bitrate deviation exceeds 1%")
	}
	if math.Abs(m.ManifestAlignmentMS) > 1.0 {
		return nil, fault.New(fault.Verification, "track alignment exceeds 1ms")
	}
	return &Result{Metrics: m, InputIndex: in, OutputIndex: out}, nil
}

// VerifyManifestArtifacts checks that a manifest's files and key invariants exist.
func VerifyManifestArtifacts(path string) (*manifest.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.NotFound, err), path, -1)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fault.At(fault.Wrap(fault.CorruptHeader, err), path, 0)
	}
	if m.Input.SHA256 == "" || m.Output.SHA256 == "" {
		return nil, fault.New(fault.Verification, "manifest is missing digests")
	}
	if m.Metrics.AudioDiffSamples != 0 {
		return nil, fault.New(fault.Verification, "manifest reports audio differences")
	}
	if math.Abs(m.Metrics.ManifestAlignmentMS) > 1 {
		return nil, fault.New(fault.Verification, "manifest alignment exceeds tolerance")
	}
	if m.Metrics.BitrateDeltaPct > 1 {
		return nil, fault.New(fault.Verification, "manifest bitrate deviation exceeds tolerance")
	}
	return &m, nil
}

func groupSamples(ss []scan.SampleRef) map[uint32][]scan.SampleRef {
	out := map[uint32][]scan.SampleRef{}
	for _, s := range ss {
		out[s.TrackID] = append(out[s.TrackID], s)
	}
	return out
}

func sliceSample(raw []byte, s scan.SampleRef) []byte {
	end := s.Offset + int64(s.Size)
	if s.Offset < 0 || end > int64(len(raw)) {
		return nil
	}
	return raw[s.Offset:end]
}

func countAudioSampleDiffs(a, b []byte) int64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	n -= n % 2
	var diffs int64
	for i := 0; i < n; i += 2 {
		if a[i] != b[i] || a[i+1] != b[i+1] {
			diffs++
		}
	}
	return diffs
}

func maxDuration(tracks []scan.Track) int64 {
	var max int64
	for _, t := range tracks {
		if t.DurationNS > max {
			max = t.DurationNS
		}
	}
	return max
}

func alignment(idx *scan.Index) (float64, error) {
	first := map[uint32]int64{}
	for _, s := range idx.Samples {
		v := s.PTSNS
		if cur, ok := first[s.TrackID]; !ok || v < cur {
			first[s.TrackID] = v
		}
	}
	offset := map[uint32]int64{}
	for _, t := range idx.Tracks {
		offset[t.ID] = t.InitialOffsetNS
	}
	var min, max int64
	n := 0
	for id, pts := range first {
		v := pts + offset[id]
		if n == 0 || v < min {
			min = v
		}
		if n == 0 || v > max {
			max = v
		}
		n++
	}
	if n < 2 {
		return 0, nil
	}
	return clock.AlignmentMS(max, min), nil
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }
