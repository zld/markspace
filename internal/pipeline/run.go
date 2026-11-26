package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"pwflow/internal/fault"
	"pwflow/internal/fingerprint"
	"pwflow/internal/media"
	"pwflow/internal/planner"
	"pwflow/internal/segment"
)

// RunResult contains the written manifest and its stable file hash.
type RunResult struct {
	Manifest       *Manifest
	ManifestPath   string
	ManifestSHA256 string
	Checkpoint     *Checkpoint
	Resumed        []int
}

type clipResult struct {
	SegmentIndex int
	SHA256       string
	Size         int64
}

// Run executes parsing, planning, ordered segment production and manifest
// writing. A checkpoint stop returns ErrStoppedAfter after durable progress.
func Run(ctx context.Context, opts Options) (*RunResult, error) {
	if opts.Context != nil {
		ctx = opts.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bundle, err := readInputs(opts)
	if err != nil {
		return nil, err
	}
	cp, err := LoadCheckpoint(opts.Checkpoint)
	if err != nil {
		return nil, err
	}
	if cp != nil && !cp.MatchesInput(bundle.digest) {
		return nil, fault.New(fault.CheckpointMismatch, "checkpoint inputs do not match current --input/--events/--rtp")
	}
	if cp == nil {
		cp = &Checkpoint{Version: 1, Input: bundle.digest, OutDir: opts.Out}
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return nil, fault.Wrap(fault.InvalidArgument, err)
	}

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 48000
	}
	fps := opts.FrameFPS
	if fps <= 0 {
		fps = 30
	}
	audio, err := analyzeAudio(ctx, opts.Input, sampleRate, bundle.repaired.Gaps)
	if err != nil {
		return nil, fault.Wrap(fault.DecodeFailed, err)
	}

	candidates := segment.BuildCandidates(bundle.events, audio.durationNS, segment.DefaultPreNS, segment.DefaultPostNS)
	keyframes := keyframesFromEBML(bundle.ebml)
	cuts := segment.Snap(candidates, keyframes, segment.DefaultKeyWindowNS)
	if len(cuts) == 0 {
		return nil, fault.New(fault.NoCandidate, "no valid fault windows within media duration %dns", audio.durationNS)
	}

	segments := make([]*SegmentResult, len(cuts))
	windows := make([]planner.WindowEstimate, len(cuts))
	for i := range cuts {
		fileName := fmt.Sprintf("segment-%03d.webm", i)
		segments[i] = &SegmentResult{Index: i, File: fileName, Cut: cuts[i]}
		windows[i] = planner.WindowEstimate{
			Index: i, Interval: cuts[i].Actual, FaultIDs: cuts[i].FaultIDs, FPS: fps,
		}
	}
	plan, err := planner.Evaluate(planner.Request{
		BudgetMiB: opts.BudgetMiB, Workers: opts.Workers,
		InputBytes: int64(len(bundle.mediaBytes)), Tracks: len(bundle.ebml.Tracks),
		Windows: windows,
	})
	if err != nil {
		return nil, err
	}

	width, height := videoDimensions(bundle.ebml)
	registry := fingerprint.NewRegistry()
	luma, frameCount, err := analyzeVideoIntoSegments(ctx, opts.Input, width, height, fps, segments, registry)
	if err != nil {
		return nil, fault.Wrap(fault.DecodeFailed, err)
	}
	for _, sil := range audio.silence.Silences {
		if idx := segmentIndexAt(segments, sil.Lo); idx >= 0 {
			segments[idx].Silences = append(segments[idx].Silences, sil)
		}
	}

	tasks := make([]Task[clipResult], 0, len(segments))
	resumed := make([]int, 0)
	pendingOrdinal := 0
	for i := range segments {
		if done, ok := cp.CompletedIndex(i); ok {
			resumed = append(resumed, i)
			if err := verifyCompletedFile(opts.Out, done); err != nil {
				return nil, err
			}
			segments[i].FileSHA256 = done.FileSHA256
			if st, statErr := os.Stat(filepath.Join(opts.Out, done.File)); statErr == nil {
				segments[i].FileSize = st.Size()
			}
			continue
		}
		idx, ordinal := i, pendingOrdinal
		pendingOrdinal++
		tasks = append(tasks, Task[clipResult]{
			Index: ordinal,
			Run: func(runCtx context.Context) (clipResult, error) {
				dst := filepath.Join(opts.Out, segments[idx].File)
				if err := media.CutWebM(runCtx, opts.Input, dst, cuts[idx].Actual.Lo, cuts[idx].Actual.Hi); err != nil {
					return clipResult{}, fault.Wrap(fault.DecodeFailed, err)
				}
				sha, size, err := fingerprint.File(dst)
				if err != nil {
					return clipResult{}, err
				}
				return clipResult{SegmentIndex: idx, SHA256: sha, Size: size}, nil
			},
		})
	}

	newCompleted := 0
	poolErr := OrderedPool(ctx, plan.Workers, tasks, func(_ int, res clipResult) error {
		i := res.SegmentIndex
		segments[i].FileSHA256 = res.SHA256
		segments[i].FileSize = res.Size
		cp.Completed = append(cp.Completed, CompletedSegment{
			Index: i, Interval: segments[i].Cut.Actual, File: segments[i].File,
			FileSHA256: res.SHA256, ResultSHA256: segments[i].FramesSHA256,
		})
		cp.ManifestSummary = checkpointSummary(nil, bundle, segments, audio)
		if err := SaveCheckpoint(opts.Checkpoint, *cp); err != nil {
			return err
		}
		newCompleted++
		if opts.StopAfter > 0 && newCompleted >= opts.StopAfter {
			return ErrStoppedAfter
		}
		return nil
	})
	if poolErr != nil {
		if errors.Is(poolErr, ErrStoppedAfter) {
			cp.ManifestSummary = checkpointSummary(nil, bundle, segments, audio)
			_ = SaveCheckpoint(opts.Checkpoint, *cp)
			return &RunResult{Checkpoint: cp}, poolErr
		}
		return nil, poolErr
	}

	segValues := make([]SegmentResult, len(segments))
	for i := range segments {
		segValues[i] = *segments[i]
	}
	status := StatusPass
	if len(bundle.ebml.Damages) > 0 || bundle.repaired.Missing > 0 || len(bundle.parseErrors) > 0 || hasUnmappedGap(bundle.repaired.Gaps) {
		status = StatusGaps
	}
	maxCutErr := int64(0)
	for _, c := range cuts {
		if c.MaxErrorNS > maxCutErr {
			maxCutErr = c.MaxErrorNS
		}
	}
	manifest := &Manifest{
		SchemaVersion: ManifestVersion, Status: status,
		Inputs: Inputs{
			MediaPath: opts.Input, EventsPath: opts.Events, RTPPath: opts.RTP,
			MediaSHA: bundle.digest.Files["media"], EventsSHA: bundle.digest.Files["events"],
			RTPSHA: bundle.digest.Files["rtp"], Combined: bundle.digest.Digest,
		},
		Plan: plan, Segments: segValues, Silences: audio.silence.Silences,
		AudioWindows: audio.windows, RTPGaps: bundle.repaired.Gaps,
		EBMLDamages: bundle.ebml.Damages, DuplicateGroups: registry.Groups(),
		Counts: Counts{
			Segments: len(segValues), SilenceIntervals: len(audio.silence.Silences),
			DuplicatePackets: bundle.repaired.Duplicates, MissingPackets: bundle.repaired.Missing,
			EBMLDamages: len(bundle.ebml.Damages), DuplicateContent: len(registry.Groups()),
			Frames: frameCount,
		},
		Alignment: Alignment{
			MaxErrorNS: maxCutErr, MaxErrorMS: float64(maxCutErr) / 1e6,
			CutMaxErrorNS: maxCutErr, ThresholdMS: 40,
		},
		LumaMean: luma, LoudnessPeakDB: audio.silence.PeakDBFS,
		BandwidthBpsEWMA: bundle.bandwidth.BpsEWMA,
		Errors:           bundle.parseErrors,
	}
	mpath := filepath.Join(opts.Out, "manifest.json")
	if err := WriteManifest(mpath, manifest); err != nil {
		return nil, err
	}
	msha, _, err := fingerprint.File(mpath)
	if err != nil {
		return nil, err
	}
	cp.ManifestSummary = checkpointSummary(manifest, bundle, segments, audio)
	cp.ManifestSummary.ManifestSHA256 = msha
	if err := SaveCheckpoint(opts.Checkpoint, *cp); err != nil {
		return nil, err
	}
	return &RunResult{
		Manifest: manifest, ManifestPath: mpath, ManifestSHA256: msha,
		Checkpoint: cp, Resumed: resumed,
	}, nil
}

func verifyCompletedFile(out string, done CompletedSegment) error {
	path := filepath.Join(out, done.File)
	sha, _, err := fingerprint.File(path)
	if err != nil {
		return fault.Wrap(fault.CheckpointCorrupt, err)
	}
	if sha != done.FileSHA256 {
		return fault.New(fault.CheckpointCorrupt, "completed segment %s hash changed", done.File)
	}
	return nil
}
