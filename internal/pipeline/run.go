package pipeline

import (
	"os"
	"path/filepath"

	"tracecheck/internal/clock"
	"tracecheck/internal/probe"
	"tracecheck/internal/rtptrace"
	"tracecheck/internal/segment"
)

// Report is everything the CLI prints for one input.
type Report struct {
	InputPath        string
	InputDigest      string
	Transitions      int
	StageFailure     *StageFailure
	RecoveryEvents   []probe.RecoveryEvent
	RTPStats         rtptrace.Stats
	MaxBoundaryDelta clock.Tick
	RerunDiff        int
	Verification     segment.VerificationReport
	Recovered        *probe.RecoveryResult
	Manifest         *segment.Manifest
	Checkpoint       Checkpoint
}

// Run executes the full stage pipeline against one input file. On a second
// invocation with the same digest, the checkpoint is loaded but every stage is
// still recomputed so the manifest can be compared for determinism.
func Run(inputPath, outDir string, plan rtptrace.Plan) (*Report, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}
	digest := Digest(data)
	state := NewState()
	// A matching checkpoint proves the input is unchanged and that a prior run
	// reached a resumable point. The stage machine itself always replays from
	// the beginning so reruns can be compared for determinism; a mismatching
	// digest is fatal and must never be reused.
	if _, loadErr := Load(outDir, digest); loadErr != nil {
		if _, missing := loadErr.(*os.PathError); !missing {
			return nil, loadErr
		}
	}

	rep := &Report{InputPath: inputPath, InputDigest: digest}

	// Stage 1: scan.
	if err := state.Advance(StageScan); err != nil {
		return nil, err
	}
	nodes, scanFaults := probe.Scan(data)
	state.Complete(StageScan)

	// Stage 2: track parsing.
	if err := state.Advance(StageTracks); err != nil {
		return nil, err
	}
	tracks, parseFaults := probe.ParseTracks(data, nodes)
	state.Complete(StageTracks)

	if f := firstFault(append(scanFaults, parseFaults...)); f != nil && f.Category == clock.CategoryBounds {
		state.Fail(StageTracks, f.Offset, f.Category, f.Detail)
		return rep.finish(state, digest, outDir)
	}

	// Stage 3: recovery.
	if err := state.Advance(StageRecover); err != nil {
		return nil, err
	}
	recovered, err := probe.Recover(data, tracks, probe.Find(nodes, "mdat"))
	if err != nil {
		cat := clock.CategoryNoSync
		if _, ok := err.(*clock.ErrOutOfRange); ok {
			cat = clock.CategoryBounds
		}
		state.Fail(StageRecover, failureOffset(err), cat, err.Error())
		rep.StageFailure = state.Failure
		_ = Save(outDir, state.snapshot(digest, recoveredSafe(recovered)))
		return rep, err
	}
	state.Complete(StageRecover)
	rep.Recovered = recovered
	rep.RecoveryEvents = recovered.Events

	// Stage 4: RTP trace (one trace per representation, aggregate stats).
	if err := state.Advance(StageTrace); err != nil {
		return nil, err
	}
	for id := range recovered.Tracks {
		pkts := rtptrace.FromTimeline(recovered.Tracks[id],
			rtptrace.TimelineOptions{SSRC: 1000 + id, StartSequence: 0})
		trace := rtptrace.Apply(pkts, plan)
		rep.RTPStats.Lost += trace.Stats.Lost
		rep.RTPStats.Duplicated += trace.Stats.Duplicated
		rep.RTPStats.Reordered += trace.Stats.Reordered
		rep.RTPStats.Delivered += trace.Stats.Delivered
	}
	state.Complete(StageTrace)

	// Stage 5: segmentation.
	if err := state.Advance(StageSegment); err != nil {
		return nil, err
	}
	alignment, err := segment.Align(recovered.Tracks)
	if err != nil {
		state.Fail(StageSegment, failureOffset(err), clock.CategoryBounds, err.Error())
		rep.StageFailure = state.Failure
		_ = Save(outDir, state.snapshot(digest, recovered.LastSafeOffset))
		return rep, err
	}
	manifest, err := segment.Build(alignment, recovered.Tracks, digest)
	if err != nil {
		return nil, err
	}
	rep.MaxBoundaryDelta = clock.Tick(manifest.MaxDelta)
	rep.Manifest = manifest
	state.Complete(StageSegment)

	// Stage 6: verification.
	if err := state.Advance(StageVerify); err != nil {
		return nil, err
	}
	trackIDs := make([]uint32, 0, len(recovered.Tracks))
	for id := range recovered.Tracks {
		trackIDs = append(trackIDs, id)
	}
	rep.Verification = segment.Verify(manifest, data, sortedIDs(trackIDs))
	if !rep.Verification.OK() {
		state.Fail(StageVerify, 0, clock.CategoryBounds, "manifest verification failed")
		_ = Save(outDir, state.snapshot(digest, recovered.LastSafeOffset))
		return rep, &clock.ErrUnrecoverable{Reason: "manifest verification failed", Offset: 0}
	}
	state.Complete(StageVerify)

	rep.Transitions = state.Transitions()
	cp := state.snapshot(digest, recovered.LastSafeOffset)
	rep.Checkpoint = cp
	if err := Save(outDir, cp); err != nil {
		return nil, err
	}
	if err := writeManifest(outDir, manifest); err != nil {
		return nil, err
	}
	return rep, nil
}

func (r *Report) finish(state *State, digest, outDir string) (*Report, error) {
	r.StageFailure = state.Failure
	r.Transitions = state.Transitions()
	_ = Save(outDir, state.snapshot(digest, 0))
	return r, &clock.ErrUnrecoverable{
		Reason: state.Failure.Detail, Offset: state.Failure.Offset,
	}
}

func firstFault(faults []*clock.OffsetError) *clock.OffsetError {
	for _, f := range faults {
		return f
	}
	return nil
}

// failureOffset extracts the original byte position from either a categorised
// OffsetError or an ErrUnrecoverable, both of which pin real input bytes.
func failureOffset(err error) clock.Offset {
	if off, ok := clock.OffsetOf(err); ok {
		return off
	}
	var ur *clock.ErrUnrecoverable
	if asUnrecoverable(err, &ur) {
		return ur.Offset
	}
	return 0
}

func asUnrecoverable(err error, target **clock.ErrUnrecoverable) bool {
	for err != nil {
		if e, ok := err.(*clock.ErrUnrecoverable); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func recoveredSafe(r *probe.RecoveryResult) clock.Offset {
	if r == nil {
		return 0
	}
	return r.LastSafeOffset
}

func sortedIDs(ids []uint32) []uint32 {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	return ids
}

func writeManifest(outDir string, m *segment.Manifest) error {
	raw, err := m.Encode()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "manifest.json"), raw, 0o644)
}
