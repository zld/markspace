package kernel

import (
	"fmt"
	"os"
)

// RunOptions configures one demo invocation.
type RunOptions struct {
	Fixture      string
	Out          string
	StopAfter    string
	Resume       bool
	MutateSource bool
	TamperOutput bool
	Silence      SilenceConfig
}

// RunResult contains all terminal-visible summaries.
type RunResult struct {
	SourceDigest    string
	Stages          []string
	ResumeStage     string
	ResumePacket    uint64
	CheckpointPath  string
	ManifestPath    string
	Wraps           int
	CutCount        int
	SilenceMS       int64
	GapEventCount   int
	InputPacket     uint64
	RTPStats        ReorderStats
	Cache           CacheDiff
	DuplicateGroups int
	Manifest        *Manifest
	Validation      ValidationReport
}

// Run executes ingest, parse, cut, checkpoint/resume, delivery, and manifest.
func Run(opts RunOptions) (*RunResult, error) {
	if opts.Out == "" {
		opts.Out = "./replay-out"
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return &RunResult{}, err
	}
	if !opts.Resume {
		// An explicit fresh run must not leave an old recovery marker that
		// could be mistaken for the result of this invocation.
		if cpPath, pathErr := CheckpointPath(opts.Out); pathErr == nil {
			if err := os.Remove(cpPath); err != nil && !os.IsNotExist(err) {
				return &RunResult{CheckpointPath: cpPath}, err
			}
		}
	}
	if opts.Resume {
		return runResume(opts)
	}
	return runFresh(opts)
}

func runFresh(opts RunOptions) (*RunResult, error) {
	fixture, err := GenerateFixture(opts.Fixture, opts.MutateSource)
	if err != nil {
		return &RunResult{}, err
	}
	res := &RunResult{SourceDigest: fixture.SourceDigest}
	res.CheckpointPath, _ = CheckpointPath(opts.Out)

	reorder := NewReorderer(ReorderConfig{Window: DefaultReorderWindow, BandwidthBps: 4_000_000})
	parser := NewTSParser()
	psi := NewPSITracker()
	builder := NewTrackBuilder(fixture.SourceDigest)
	var gaps []GapEvent
	var inputPacket uint64

	handleTS := func(payload []byte, packet uint64) error {
		packets, tsGaps, err := parser.ParseDatagram(payload, packet)
		if err != nil {
			return err
		}
		gaps = append(gaps, tsGaps...)
		for _, pkt := range packets {
			if pkt.PID == 0x0000 || pkt.PID == fixturePMTPID {
				if _, err := psi.HandleTSPayload(pkt.PID, pkt.Payload); err != nil {
					return err
				}
			}
			if err := builder.AddTSPacket(pkt); err != nil {
				return err
			}
		}
		return nil
	}

	for i, dg := range fixture.Datagrams {
		inputPacket = uint64(i + 1)
		released, rtpGaps, err := reorder.Ingest(dg.Raw, inputPacket)
		if err != nil {
			res.InputPacket = inputPacket
			return res, err
		}
		gaps = append(gaps, rtpGaps...)
		for _, pkt := range released {
			if err := handleTS(pkt.Payload, inputPacket); err != nil {
				res.InputPacket = inputPacket
				return res, err
			}
		}
	}
	flushed, flushGaps := reorder.FlushLosses(inputPacket)
	gaps = append(gaps, flushGaps...)
	for _, pkt := range flushed {
		if err := handleTS(pkt.Payload, inputPacket); err != nil {
			return res, err
		}
	}

	res.RTPStats = reorder.Stats()
	res.InputPacket = inputPacket
	prog := builder.Build(gaps, parser.ClockState().Wraps, inputPacket)
	res.Wraps = prog.Wraps
	res.GapEventCount = len(prog.Gaps)

	silenceCfg := opts.Silence
	if silenceCfg.MinDurationMS == 0 && silenceCfg.ThresholdAmplitude == 0 {
		silenceCfg = DefaultSilenceConfig()
	}
	segments, err := CutProgram(prog, CutConfig{Silence: silenceCfg, PSI: psi})
	if err != nil {
		return res, err
	}
	for _, seg := range segments {
		fp, err := SegmentFingerprint(seg)
		if err != nil {
			return res, err
		}
		seg.ContentHash = fp
	}
	res.CutCount = len(segments)
	res.SilenceMS = sumSegmentSilence(segments)

	if opts.StopAfter == StageCut {
		// A manifest from an older completed run must not masquerade as the
		// not-yet-produced manifest for this checkpoint.
		if manifestPath, pathErr := ManifestPath(opts.Out); pathErr == nil {
			if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
				return res, err
			}
		}
		cp := &Checkpoint{
			Stage: StageCut, Fixture: opts.Fixture, MutateSource: opts.MutateSource,
			SourceDigest: fixture.SourceDigest, InputPacket: inputPacket,
			Clock: parser.ClockState(), Wraps: prog.Wraps,
			TrackTimes: TrackCheckpoints(prog), Gaps: prog.Gaps, Segments: segments,
			SilenceMS: res.SilenceMS, CutCount: len(segments), GapEventCount: len(prog.Gaps),
		}
		path, err := SaveCheckpoint(opts.Out, cp)
		res.CheckpointPath = path
		res.Stages = []string{StageIngest, StageParse, StageCut}
		if err != nil {
			return res, err
		}
		return res, &CheckpointStop{Stage: StageCut, Packet: inputPacket}
	}
	if opts.StopAfter != "" {
		return res, &MediaError{Stage: "cli", Code: "unsupported_stop_stage", Message: "only --stop-after cut is recoverable", Recoverable: false}
	}

	res.Stages = []string{StageIngest, StageParse, StageCut, StageManifest}
	return completeDelivery(opts, res, fixture.SourceDigest, opts.Fixture, opts.MutateSource,
		prog.Gaps, prog.Wraps, res.SilenceMS, segments)
}

func runResume(opts RunOptions) (*RunResult, error) {
	cp, path, err := LoadCheckpoint(opts.Out)
	res := &RunResult{CheckpointPath: path}
	if err != nil {
		return res, err
	}
	fixture, err := GenerateFixture(cp.Fixture, cp.MutateSource)
	if err != nil {
		return res, err
	}
	if fixture.SourceDigest != cp.SourceDigest {
		return res, &MediaError{
			Stage: "resume", Code: "source_digest_mismatch",
			Message:     "checkpoint source digest does not match regenerated source",
			Recoverable: false,
		}
	}
	res.SourceDigest = cp.SourceDigest
	res.ResumeStage = StageManifest
	res.ResumePacket = cp.InputPacket
	res.InputPacket = cp.InputPacket
	res.Wraps = cp.Wraps
	res.CutCount = cp.CutCount
	res.SilenceMS = cp.SilenceMS
	res.GapEventCount = cp.GapEventCount
	if res.CutCount == 0 {
		res.CutCount = len(cp.Segments)
	}
	if res.GapEventCount == 0 {
		res.GapEventCount = len(cp.Gaps)
	}
	res.Stages = []string{StageManifest}
	return completeDelivery(opts, res, cp.SourceDigest, cp.Fixture, cp.MutateSource, cp.Gaps, cp.Wraps, cp.SilenceMS, cp.Segments)
}

func completeDelivery(opts RunOptions, res *RunResult, sourceDigest, fixtureName string, mutated bool, gaps []GapEvent, wraps int, silenceMS int64, segments []*Segment) (*RunResult, error) {
	diff, err := DeliverSegments(opts.Out, sourceDigest, segments)
	if err != nil {
		return res, err
	}
	res.Cache = diff
	dups, err := FindDuplicateSegments(segments)
	if err != nil {
		return res, err
	}
	res.DuplicateGroups = len(dups)

	m := &Manifest{
		Source: ManifestSource{Digest: sourceDigest, Fixture: fixtureName, Mutated: mutated},
		Stages: res.Stages, Wraps: wraps, CutCount: len(segments),
		SilenceMS: silenceMS, GapEvents: gaps,
	}
	for _, seg := range segments {
		m.Segments = append(m.Segments, ManifestSegment{
			Index: seg.Index, ID: seg.ID, File: seg.File, SHA256: seg.FileSHA256,
			SizeBytes: seg.SizeBytes, Time: seg.Time, ContentHash: seg.ContentHash,
			EventIDs: seg.EventIDs, GapIDs: seg.GapIDs, Silence: seg.Silence,
		})
	}
	path, err := WriteManifest(opts.Out, m)
	res.ManifestPath = path
	if err != nil {
		return res, err
	}
	res.Manifest = m
	res.Validation = ValidateManifest(opts.Out, m)

	if opts.TamperOutput && len(m.Segments) > 0 {
		target, err := SafeJoin(opts.Out, m.Segments[0].File)
		if err != nil {
			return res, err
		}
		if err := os.WriteFile(target, []byte("tampered-final-byte"), 0o644); err != nil {
			return res, err
		}
		res.Validation = ValidateManifest(opts.Out, m)
	}
	if !res.Validation.Valid {
		return res, &MediaError{
			Stage: "manifest", Code: "manifest_validation_failed",
			Message:     fmt.Sprintf("%d manifest checks failed", res.Validation.ErrorCount),
			Recoverable: false,
		}
	}
	return res, nil
}

func sumSegmentSilence(segments []*Segment) int64 {
	var total int64
	for _, seg := range segments {
		total += SilenceTotalMS(seg.Silence)
	}
	return total
}
