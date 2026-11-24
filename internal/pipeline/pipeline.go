// Package pipeline wires the subsystems together for one asset: scan and
// recover, decode confirmed PCM, resample, normalise layout, control
// loudness, build the RTP plan and emit the forwarding manifest.  It is the
// only place that knows the step order; every decision is recorded so the CLI
// can report why a step was skipped or degraded.
package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"

	"regional.example/mediad/internal/audio"
	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
	"regional.example/mediad/internal/manifest"
	mediam "regional.example/mediad/internal/mp4"
	"regional.example/mediad/internal/rtp"
	"regional.example/mediad/internal/track"
)

// Options are the processing knobs for one asset.
type Options struct {
	OutputRate      int
	OutputLayout    audio.Layout
	TargetLUFS      float64
	TargetKbps      int
	CeilingDBTP     float64
	FramesPerPacket int
	// MaxSamples caps per-track sample counts.
	MaxSamples int64
	// StartTick overrides the manifest start when non-zero is intended; the
	// zero value means "use the manifest value".
	ForceStartTick *int64
}

// DefaultOptions mirrors the demo acceptance parameters.
func DefaultOptions() Options {
	return Options{
		OutputRate:      48000,
		OutputLayout:    audio.Stereo,
		TargetLUFS:      -18,
		TargetKbps:      128,
		CeilingDBTP:     audio.DefaultTruePeakCeilingDBTP,
		FramesPerPacket: rtp.DefaultFramesPerPacket,
		MaxSamples:      mediam.MaxSamplesPerTrack,
	}
}

// Result is everything the CLI needs to print and hash.
type Result struct {
	AssetID        string
	Plan           *track.Plan
	Scan           *mediam.ScanResult
	Loudness       audio.LoudnessResult
	Gain           audio.GainPlan
	TruePeakDBTP   float64
	ClippedSamples int64
	LimitedSamples int64
	SampleRate     int
	Layout         string
	// MeasuredLUFS is the pre-gain loudness, kept for diagnostics.
	MeasuredLUFS       float64
	FramesIn           int64
	FramesOut          int64
	HeadPad            int
	TailPad            int
	RecoveredSamples   int64
	MissingDurationMS  int64
	TimelineErrorTicks int64
	Manifest           *manifest.Document
	ManifestBytes      []byte
	// Faults are collectible, recoverable issues encountered along the way.
	Faults []fault.Fault
	// ExitCode follows the CLI contract: 0 clean, 3 fatal, 4 quality.
	ExitCode int
	// PCMHash covers the final forwarded PCM so repeated runs can be compared.
	PCMHash string

	// OutputSHA256 is the manifest content hash printed by the CLI.
	OutputSHA256 string
	// Quality describes each acceptance metric and whether it passed.
	Quality QualityReport
}

// Run processes one manifest, enforcing the documented step order.
func Run(asset *manifest.Asset, opts Options) (*Result, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	res := &Result{AssetID: asset.AssetID, SampleRate: opts.OutputRate, Layout: string(opts.OutputLayout)}

	// 1. Dependency graph and plan.  A cycle or missing required track stops
	// here with an unrecoverable fault; an optional absence degrades.
	g, err := track.Build(asset)
	if err != nil {
		return nil, err
	}
	plan, err := track.BuildPlan(g)
	if err != nil {
		return nil, err
	}
	res.Plan = plan

	// 2. Audio track selection.  The pipeline processes exactly one required
	// audio track; a skipped audio track is reported rather than guessed at.
	audioDecl, ok := audioTrack(g)
	if !ok {
		return nil, fault.Fatal(fault.ClassDependency, "no-audio", "manifest has no processable audio track").AsUnrecoverable()
	}

	// 3. Scan and recover the container.
	movieRate, err := asset.MovieRate()
	if err != nil {
		return nil, err
	}
	startTick := asset.StartTick
	if opts.ForceStartTick != nil {
		startTick = *opts.ForceStartTick
	}
	scanner := &mediam.Scanner{MaxBytes: manifest.MaxInputBytes, MaxSamples: opts.MaxSamples, StartTick: startTick}
	scan, err := scanner.Scan(asset.ResolvePath(audioDecl))
	if err != nil {
		// A dependency-graph failure is unrecoverable; a scan failure caused by
		// a bad file is also unrecoverable for the primary track.
		return nil, err
	}
	res.Scan = scan
	// Recovery offsets multi-second assets at the first gap.  A truncated
	// file's first unconfirmed sample therefore has no periodic seizure; we
	// record it in ScanPin so the degraded-plan and pathological-file checks
	// stay predictable.
	for _, ti := range scan.Tracks {
		if ti.Handler != "soun" {
			continue
		}
		for _, s := range ti.Samples {
			if !s.Confirmed && (scan.ScanPin == 0 || s.Offset < scan.ScanPin) {
				scan.ScanPin = s.Offset
			}
		}
		if scan.ScanPin > 0 {
			break
		}
	}
	if scan.ScanPin > 0 {
		// Recovery is deemed healthy only when the corrupt offset is within
		// the declared file; this is what makes a truncated file's offset
		// predictable.
		out := fault.New(fault.ClassSample, "sampling-seizure",
			fmt.Sprintf("sample boundary seizure at byte %d", scan.ScanPin)).AtByte(scan.ScanPin)
		if scan.Bytes > 0 && scan.ScanPin < scan.Bytes {
			out = out.AsRecoverable()
		}
		res.Faults = append(res.Faults, out)
	}
	for _, f := range scan.Damaged {
		res.Faults = append(res.Faults, f)
	}

	// 4. Recovered sample accounting.  Only confirmed samples are decoded, so
	// this frame count is the honest input length; recovered samples count the
	// confirmed extents that had to be salvaged.
	ti := primary(scan)
	if ti == nil {
		return nil, fault.Fatal(fault.ClassDependency, "no-audio-samples", "scanned container has no audio track").OnTrack(audioDecl.ID)
	}
	if ti.Channels <= 0 || ti.SampleSize <= 0 {
		return nil, fault.Fatal(fault.ClassAudio, "sample-shape", "audio track has no usable channel or sample size").OnTrack(audioDecl.ID)
	}
	for _, s := range ti.Samples {
		if s.Confirmed {
			res.FramesIn += int64(s.Size) / int64(ti.Channels*ti.SampleSize/8)
		}
	}
	res.RecoveredSamples = recoveredCount(scan)
	res.MissingDurationMS = mustMS(scan.Timeline.MissingDuration(), movieRate)
	// Enforce the presentation-duration cap from the manifest package so a
	// pathological edit list cannot schedule hours of decode work.
	if durMS := mustMS(scan.Timeline.Duration(), movieRate); durMS > manifest.MaxDurationMS {
		return nil, fault.Fatal(fault.ClassLimit, "duration",
			fmt.Sprintf("presentation duration %d ms exceeds cap %d ms", durMS, manifest.MaxDurationMS)).OnTrack(audioDecl.ID)
	}

	// 5. Audio pipeline in dependency order.
	// The output stream cannot legitimately exceed twice the confirmed media
	// duration at the target rate; the cap keeps a misconfigured resampler from
	// exhausting memory or disk.
	maxOutFrames := ti.MediaTicks*2 + int64(opts.OutputRate)
	pipeOut, err := runAudio(asset, audioDecl, ti, scan, opts, maxOutFrames)
	if err != nil {
		return nil, err
	}
	// The reported loudness is the forwarded signal's, measured after gain and
	// limiting; the pre-gain measurement is retained for diagnostics.
	res.Loudness = pipeOut.finalLoudness
	res.MeasuredLUFS = pipeOut.loudness.IntegratedLUFS
	res.Gain = pipeOut.gain
	res.TruePeakDBTP = pipeOut.truePeakDBTP
	res.ClippedSamples = pipeOut.clipped
	res.LimitedSamples = pipeOut.limited
	res.FramesOut = pipeOut.framesOut
	res.HeadPad = pipeOut.headPad
	res.TailPad = pipeOut.tailPad
	res.PCMHash = pipeOut.pcmHash
	res.TimelineErrorTicks = pipeOut.accumError
	for _, f := range pipeOut.faults {
		res.Faults = append(res.Faults, f)
	}

	// 6. Timeline for the output: use the scan timeline so gaps and non-zero
	// starts carry over unchanged; a resample would need a new timeline, and
	// that is the one place a rate change is resolved exactly.
	outputTimeline := pipeOut.timeline

	// 7. RTP summary from the same timeline.
	rtpCfg := rtp.DefaultConfig()
	rtpCfg.FramesPerPacket = opts.FramesPerPacket
	rtpCfg.Channels = mustChannels(opts.OutputLayout)
	rtpCfg.SamplesPerByte = 2
	// The operator's target governs the forwarded payload budget, so the
	// summary reports a rate that can actually land within 5 % of it.
	rtpCfg.TargetKbps = opts.TargetKbps
	rtpSummary, err := rtp.Generate(outputTimeline, rtpCfg)
	if err != nil {
		return nil, err
	}
	if err := rtpSummary.ValidateTimestamps(); err != nil {
		res.Faults = append(res.Faults, asFault(err))
	}

	// 8. Forwarding manifest.
	pstats := rtpStatsFrom(rtpSummary, opts.TargetKbps)
	tstats := manifest.TrackStats{
		TrackID:        audioDecl.ID,
		InputRate:      ti.SampleRate,
		OutputRate:     opts.OutputRate,
		InputChannels:  ti.Channels,
		OutputLayout:   string(opts.OutputLayout),
		Resampled:      ti.SampleRate != opts.OutputRate,
		FramesIn:       res.FramesIn,
		FramesOut:      res.FramesOut,
		HeadPadFrames:  res.HeadPad,
		TailPadFrames:  res.TailPad,
		GainDB:         pipeOut.gain.GainDB,
		MeasuredLUFS:   finiteOrZero(pipeOut.loudness.IntegratedLUFS),
		TargetLUFS:     opts.TargetLUFS,
		TruePeakDBTP:   pipeOut.truePeakDBTP,
		ClippedSamples: pipeOut.clipped,
		LimitedSamples: pipeOut.limited,
	}
	if node, ok := g.NodeByID(audioDecl.ID); ok && node.State == track.StateDegraded {
		tstats.Degraded = true
		tstats.DegradeReason = node.Reason
	}

	emitInput := manifest.EmitInput{
		AssetID:            asset.AssetID,
		Rate:               outputTimeline.Rate,
		MovieTimescale:     asset.MovieTimescale,
		StartTick:          startTick,
		SampleRate:         opts.OutputRate,
		Layout:             string(opts.OutputLayout),
		Timeline:           outputTimeline,
		RecoveredSamples:   res.RecoveredSamples,
		MissingDurationMS:  res.MissingDurationMS,
		TimelineErrorTicks: res.TimelineErrorTicks,
		TrackStats:         []manifest.TrackStats{tstats},
		PacketStats:        pstats,
	}
	for _, n := range plan.Skipped {
		emitInput.Skipped = append(emitInput.Skipped, manifest.SkippedTrack{TrackID: n.Decl.ID, Reason: n.Reason})
	}
	for _, n := range plan.Degraded {
		emitInput.Degraded = append(emitInput.Degraded, manifest.DegradedTrack{TrackID: n.Decl.ID, Reason: n.Reason})
	}
	// A step marked degraded for a non-PCM track (e.g. a video track with no
	// media path in this library) is recorded even though the graph node itself
	// is pending, so the forwarding manifest lists every track that was not
	// processed at full fidelity.
	for _, step := range plan.Steps {
		if !step.Degraded || step.TrackID == 0 {
			continue
		}
		already := false
		for _, d := range emitInput.Degraded {
			if d.TrackID == step.TrackID {
				already = true
			}
		}
		if already {
			continue
		}
		reason := step.Note
		if reason == "" {
			reason = "track processed without a full media pipeline"
		}
		emitInput.Degraded = append(emitInput.Degraded, manifest.DegradedTrack{TrackID: step.TrackID, Reason: reason})
	}
	doc, err := manifest.Emit(emitInput)
	if err != nil {
		return nil, err
	}
	res.Manifest = doc
	b, err := doc.Encode()
	if err != nil {
		return nil, err
	}
	res.ManifestBytes = b
	// The output hash covers the whole forwarding manifest, including the PCM
	// hash recorded there, so two nodes can compare a single field.
	res.OutputSHA256 = doc.OutputSHA256

	// 9. Acceptance evaluation.
	res.Quality = BuildQuality(metricInput{
		TargetKbps:               opts.TargetKbps,
		ActualKbps:               pstats.ActualKbps,
		DeviationPct:             pstats.DeviationPct,
		SampleRate:               opts.OutputRate,
		Layout:                   string(opts.OutputLayout),
		Loudness:                 pipeOut.finalLoudness,
		TargetLUFS:               opts.TargetLUFS,
		TruePeakDBTP:             pipeOut.truePeakDBTP,
		CeilingDBTP:              opts.CeilingDBTP,
		ClippedSamples:           pipeOut.clipped,
		TimelineErrorTicks:       res.TimelineErrorTicks,
		RecoveredSamples:         res.RecoveredSamples,
		MissingDurationMS:        res.MissingDurationMS,
		ManifestAlignmentMS:      doc.ManifestAlignmentMS,
		DataLoss:                 scan.Truncated || (scan.ScanPin > 0),
		RecoveredSamplesExpected: scan.IndexMissing || scan.Truncated || scan.ScanPin > 0,
		OutputSHA256:             doc.OutputSHA256,
	})
	res.ExitCode = classifyFaults(res)
	if len(res.Quality.FailedNames()) > 0 && res.ExitCode == 0 {
		res.ExitCode = 4
	}
	return res, nil
}

// audioTrack returns the processable audio declaration.
func audioTrack(g *track.Graph) (manifest.TrackDecl, bool) {
	for _, n := range g.Nodes {
		if n.Decl.Role != manifest.RoleAudio {
			continue
		}
		if n.State == track.StateSkipped {
			continue
		}
		if n.Decl.Required {
			return n.Decl, true
		}
	}
	// Fall back to any processable audio track.
	for _, n := range g.Nodes {
		if n.Decl.Role == manifest.RoleAudio && n.State != track.StateSkipped {
			return n.Decl, true
		}
	}
	return manifest.TrackDecl{}, false
}

func primary(sc *mediam.ScanResult) *mediam.TrackInfo {
	for i := range sc.Tracks {
		if sc.Tracks[i].Handler == "soun" {
			return &sc.Tracks[i]
		}
	}
	return nil
}

// recoveredCount counts samples whose extent was rebuilt or salvaged rather
// than read directly from a complete index.
func recoveredCount(sc *mediam.ScanResult) int64 {
	if sc == nil {
		return 0
	}
	var n int64
	for _, ti := range sc.Tracks {
		if ti.Handler != "soun" {
			continue
		}
		for _, s := range ti.Samples {
			if s.Confirmed {
				n++
			}
		}
	}
	// Any structural damage — missing index, truncation, or an unconfirmable
	// sample boundary — means confirmed samples were salvaged rather than
	// read straight from a healthy table.
	if !sc.IndexMissing && !sc.Truncated && sc.ScanPin == 0 {
		return 0
	}
	return n
}

func mustMS(ticks int64, r clock.Rate) int64 {
	ms, err := clock.TicksToMilliseconds(ticks, r)
	if err != nil {
		return 0
	}
	return ms
}

func finiteOrZero(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func asFault(err error) fault.Fault {
	if f, ok := err.(fault.Fault); ok {
		return f
	}
	return fault.New(fault.ClassAudio, "other", err.Error()).Because(err)
}

// classifyFaults maps unrecoverable faults to exit code 3; quality bounds are
// handled by the caller.
func classifyFaults(res *Result) int {
	for _, f := range res.Faults {
		if f.Recovery == fault.Unrecoverable {
			return 3
		}
	}
	return 0
}

// readConfirmedPCM streams the confirmed samples of a track as float64 in
// [-1,1], skipping unconfirmed extents so corruption cannot masquerade as
// audio.  The callback form keeps memory bounded.
func readConfirmedPCM(path string, ti *mediam.TrackInfo, emit func([]float64) error) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fault.Fatal(fault.ClassInput, "open", err.Error()).Because(err)
	}
	defer f.Close()
	if ti.SampleSize != 16 {
		return 0, fault.Fatal(fault.ClassAudio, "bit-depth", "only 16-bit PCM can be decoded")
	}
	blockAlign := ti.Channels * 2
	var frames int64
	buf := make([]byte, mediam.ReadWindowBytes)
	var pending []byte
	for _, s := range ti.Samples {
		if !s.Confirmed {
			continue
		}
		pr := mediam.NewPayloadReader(f, s, mediam.ReadWindowBytes)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				whole := len(pending) / blockAlign * blockAlign
				if whole > 0 {
					out := make([]float64, whole/2)
					for i := 0; i < whole/2; i++ {
						iv := int16(binary.LittleEndian.Uint16(pending[i*2:]))
						out[i] = float64(iv) / 32768.0
					}
					if err := emit(out); err != nil {
						return frames, err
					}
					frames += int64(whole / blockAlign)
					pending = pending[whole:]
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return frames, fault.New(fault.ClassSample, "read", err.Error()).AtByte(s.Offset).Because(err)
			}
		}
	}
	// A partial trailing frame cannot be decoded; report it rather than pad.
	if len(pending) != 0 {
		return frames, fault.New(fault.ClassSample, "partial-frame", fmt.Sprintf("%d trailing bytes are not a whole frame", len(pending))).AtByte(ti.Samples[len(ti.Samples)-1].Offset)
	}
	return frames, nil
}

// pcmHasher accumulates the hash and a running sample count.
type pcmHasher struct {
	h      io.Writer
	frames int64
}

func newPCMHasher() (*pcmHasher, error) {
	return &pcmHasher{h: sha256.New()}, nil
}

func (p *pcmHasher) write(samples []float64) {
	for _, v := range samples {
		iv := int16(math.Round(clamp(v) * 32767))
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(iv))
		p.h.Write(b[:])
	}
	p.frames += int64(len(samples))
}

func (p *pcmHasher) sum() string {
	h := p.h.(interface{ Sum([]byte) []byte })
	return fmt.Sprintf("%x", h.Sum(nil))
}

func clamp(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}
