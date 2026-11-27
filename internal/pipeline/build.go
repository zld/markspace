package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"example.com/assetpack/internal/audio"
	"example.com/assetpack/internal/clock"
	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/media"
	"example.com/assetpack/internal/migrate"
	"example.com/assetpack/internal/ts"
)

// buildAssets performs the parse and audio stages.  It reads the legacy
// manifest, parses each referenced transport stream, normalizes the recovered
// audio, and encodes the container.
func buildAssets(opts Options, journal *migrate.Journal) (*Candidate, []AssetResult, error) {
	legacyPath := filepath.Join(opts.InputDir, "manifest.v1.json")
	f, err := os.Open(legacyPath)
	if err != nil {
		return nil, nil, fault.New(fault.StageManifest, fault.KindIO,
			"open legacy manifest: %v", err)
	}
	legacy, _, err := (&manifest.Adapter{}).ReadFile(f)
	f.Close()
	if err != nil {
		return nil, nil, err
	}
	if legacy == nil {
		return nil, nil, fault.New(fault.StageManifest, fault.KindUnknownVer,
			"input manifest %s is not a legacy v1 manifest", legacyPath)
	}
	var assets []AssetResult
	var faults *fault.List
	for _, spec := range legacy.Assets {
		res, f, err := processAsset(opts, spec)
		if faults == nil {
			faults = &fault.List{}
		}
		faults.Merge(f)
		if err != nil {
			return nil, assets, err
		}
		assets = append(assets, res)
	}
	candidate := &Candidate{Legacy: legacy, Faults: faults}
	if len(assets) == 0 {
		return nil, nil, fault.New(fault.StageManifest, fault.KindMissing, "legacy manifest lists no assets")
	}
	return candidate, assets, nil
}

// processAsset parses one transport stream and normalizes its audio.  Every
// parse-side observation (damaged packets, PSI version changes) is returned
// as a structured fault so the report names each class with its offset.
func processAsset(opts Options, spec manifest.V1Asset) (AssetResult, *fault.List, error) {
	faults := &fault.List{}
	res := AssetResult{
		Name: spec.Name, SourceRate: spec.Rate, SourceChan: spec.Channels,
		Artifact: spec.Name + ".m4a", Display: spec.Name + ".m4a",
		ParseOffset: -1, ParseSample: -1,
	}
	path := filepath.Join(opts.InputDir, filepath.Base(spec.Path))
	data, err := os.ReadFile(path)
	if err != nil {
		return res, faults, fault.New(fault.StageParse, fault.KindIO,
			"read %s: %v", filepath.Base(spec.Path), err)
	}
	res.SourceDigest = sha256Hex(data)
	stream := ts.Parse(data)
	psi := ts.ParsePSI(stream)
	faults.Merge(stream.Faults)
	for _, ev := range psi.Events {
		if ev.From == ev.To && ev.Note == "" {
			continue // first appearance of a table, not a version change
		}
		faults.Add(fault.New(fault.StageParse, fault.KindPSIUpdate,
			"%s: %s version %d -> %d took effect on PID %d (%d streams)", spec.Name, ev.Table, ev.From, ev.To, ev.PID, ev.Streams).
			AtOffset(ev.Offset).Recover(true))
	}
	pmt := psi.Current
	if pmt == nil {
		return res, faults, fault.New(fault.StageParse, fault.KindPSIInvalid,
			"%s: no usable PMT was found", spec.Name).AtOffset(0)
	}
	res.PMTVersion = pmt.Version
	audioStream, ok := pmt.AudioStream()
	if !ok {
		return res, faults, fault.New(fault.StageParse, fault.KindPSIInvalid,
			"%s: PMT version %d advertises no audio elementary stream", spec.Name, pmt.Version)
	}
	frameBytes := spec.Channels * 2
	element := ts.DemuxAudio(stream, audioStream.PID, frameBytes)
	res.SourceFrames = element.SampleCount(frameBytes)
	pcmBytes, err := element.DecodePCM16LE(spec.Channels)
	if err != nil {
		return res, faults, err
	}
	if len(pcmBytes) == 0 {
		return res, faults, fault.New(fault.StageParse, fault.KindMissing,
			"%s: no audio payload recovered (PID %d)", spec.Name, audioStream.PID)
	}

	// The parse boundary is one byte past the last packet that contributed
	// trusted audio, expressed both on the input clock and on the output
	// sample clock so either can be used to resume.
	lastOffset, _ := lastTrusted(stream, audioStream.PID)
	res.ParseOffset = lastOffset
	res.ParseSample = res.SourceFrames * int64(opts.Target.Format.Rate) / int64(spec.Rate)
	for _, p := range stream.Packets {
		if p.PID == pmt.PCRPID && p.HasPCR {
			res.PCRWraps = p.PCRWraps
		}
	}

	in, partial, err := audio.FromPCM16LE(pcmBytes, spec.Rate, layoutOf(spec.Channels))
	if err != nil {
		return res, faults, err
	}
	if partial {
		res.Report.Valid = false
		return res, faults, fault.New(fault.StageAudio, fault.KindTruncated,
			"%s: source ends on a partial sample frame", spec.Name).
			AtSample(int64(in.Frames())).Recover(true)
	}

	// Translate source discontinuities onto the output clock.
	srcGaps := element.SourceGaps(spec.Rate, frameBytes)
	gaps := make([]audio.Gap, 0, len(srcGaps))
	for _, g := range srcGaps {
		start, err := clock.Rescale(g.StartSample, int64(spec.Rate), int64(opts.Target.Format.Rate))
		if err != nil {
			return res, faults, err
		}
		length, err := clock.Rescale(g.LengthSample, int64(spec.Rate), int64(opts.Target.Format.Rate))
		if err != nil {
			return res, faults, err
		}
		gaps = append(gaps, audio.Gap{Start: start, Length: length, Offset: g.SourceOffset})
	}
	gaps = audio.SortGaps(gaps)

	norm, err := audio.Normalize(in, opts.Target, gaps, audio.Options{MaxGapSamples: opts.MaxGapSample})
	if err != nil {
		return res, faults, err
	}
	res.Report = norm.Report
	res.Gaps = gaps

	container, index, err := media.Write(norm.PCM, opts.CreationSeconds)
	if err != nil {
		return res, faults, err
	}
	if err := index.Validate(norm.Report.OutFrames, opts.Target.Format.Rate, container); err != nil {
		return res, faults, err
	}
	res.Index = index
	res.Container = container
	// Artifacts are content-addressed inside the release directory.  Two
	// consecutive releases then share every unchanged artifact instead of
	// overwriting it in place, and an interrupted commit can never leave an
	// old manifest pointing at bytes that belong to the new release.
	res.Artifact = index.Digest + ".m4a"
	return res, faults, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func layoutOf(channels int) audio.Layout {
	switch channels {
	case 1:
		return audio.LayoutMono
	case 2:
		return audio.LayoutStereo
	case 6:
		return audio.Layout5_1
	}
	// Unknown source layouts are rejected by FromPCM16LE via the layout's
	// zero channel count; callers get an invalid-arg fault.
	return audio.Layout(fmt.Sprintf("channels-%d", channels))
}

func lastTrusted(s *ts.Stream, pid uint16) (int64, int64) {
	var offset int64 = -1
	var frames int64
	if s == nil {
		return offset, frames
	}
	for _, p := range s.Packets {
		if p.PID == pid && p.Trusted && p.Payload != nil {
			offset = p.Offset
		}
	}
	return offset, frames
}

// Candidate is the freshly built v2 manifest plus the legacy source it came
// from.
type Candidate struct {
	Legacy *manifest.V1
	Faults *fault.List
}

// resolvedFrom converts per-asset results into the adapter's view.
func resolvedFrom(assets []AssetResult, legacy *manifest.V1) []manifest.Resolved {
	out := make([]manifest.Resolved, 0, len(assets))
	for _, a := range assets {
		out = append(out, manifest.Resolved{
			Name: a.Name, SHA256: a.Digest(), SourceSHA256: a.SourceDigest,
			Bytes: int64(len(a.Container)), Artifact: a.Artifact,
			Frames: a.Report.OutFrames, LoudnessLUFS: a.Report.OutLoudnessLUFS,
			Peak: a.Report.OutPeak, GapSamples: a.Report.GapSamples,
			Media: intervalsFor(a.Gaps, a.Report.OutFrames),
		})
	}
	return out
}

// intervalsFor converts output-clock gaps into present-media intervals.
// Intervals plus declared gaps always cover exactly the frame count.
func intervalsFor(gaps []audio.Gap, frames int64) []manifest.Interval {
	merged := audio.SortGaps(gaps)
	var out []manifest.Interval
	cursor := int64(0)
	for _, g := range merged {
		start := g.Start
		if start > frames {
			start = frames
		}
		if start > cursor {
			out = append(out, manifest.Interval{StartSample: cursor, LengthSamples: start - cursor, SourceOffset: g.Offset})
		}
		cursor = start + g.Length
		if cursor > frames {
			cursor = frames
		}
	}
	if cursor < frames {
		out = append(out, manifest.Interval{StartSample: cursor, LengthSamples: frames - cursor})
	}
	if len(out) == 0 {
		out = append(out, manifest.Interval{StartSample: 0, LengthSamples: frames})
	}
	return out
}
