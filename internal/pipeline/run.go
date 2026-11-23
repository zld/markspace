// Package pipeline wires ingest, timeline, transform, checkpoint and verify
// into one runnable flow used by the command line entry point.
package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"mediacore/internal/checkpoint"
	"mediacore/internal/fault"
	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
	"mediacore/internal/timeline"
	"mediacore/internal/transform"
	"mediacore/internal/verify"
)

// Config is the run configuration. It is hashed for checkpoint validation.
type Config struct {
	Seed           int64    `json:"seed"`
	Tracks         int      `json:"tracks"`
	Fragments      int      `json:"fragments"`
	Faults         []string `json:"faults"`
	CheckpointPath string   `json:"checkpoint_path"`
	Resume         bool     `json:"resume"`
	FrameWidth     int      `json:"frame_width"`
	FrameHeight    int      `json:"frame_height"`
	Timescale      uint32   `json:"timescale"`
	// FramesOutput is the previously exported frame file. It is never part of
	// the configuration digest; it is only used to name the artifact that a
	// refused resume must leave untouched.
	FramesOutput string `json:"-"`
}

// Input is one named input file held in memory.
type Input struct {
	Name string
	Data []byte
}

// Result is everything a caller needs to report and verify.
type Result struct {
	Config           Config                     `json:"config"`
	Inputs           []Input                    `json:"-"`
	InputDigest      string                     `json:"input_digest"`
	ConfigDigest     string                     `json:"config_digest"`
	Init             []*ingest.InitInfo         `json:"init"`
	Fragments        []ingest.Fragment          `json:"fragments"`
	TrackCount       int                        `json:"tracks"`
	Merged           []timeline.Sample          `json:"samples"`
	Gaps             []timeline.Gap             `json:"gaps"`
	Repair           *transform.RepairReport    `json:"repair"`
	Repaired         []*transform.RepairedTrack `json:"repaired_tracks"`
	Frames           *transform.FrameSet        `json:"frames"`
	Manifest         *verify.Manifest           `json:"manifest"`
	Failures         []verify.Failure           `json:"invariant_failures"`
	Errors           []*fault.Error             `json:"errors"`
	Faults           int                        `json:"faults"`
	FirstErrorOffset int64                      `json:"first_error_offset"`
	FirstErrorCode   fault.Code                 `json:"first_error_code,omitempty"`
	InjectedOffsets  []int64                    `json:"injected_offsets"`
	Recovered        bool                       `json:"recovered"`
	Resumed          bool                       `json:"resumed"`
	ResumeMS         float64                    `json:"resume_ms"`
	LastStage        checkpoint.Stage           `json:"last_stage"`
	Duration         int64                      `json:"duration"`
	FrameCount       int                        `json:"frame_count"`
	FragmentsSeen    int                        `json:"fragments_seen"`
	FragmentTotal    int                        `json:"fragment_total"`
	ReusedFragments  int                        `json:"reused_fragments"`
	ingestErrors     []*fault.Error
	ParseErrors      int     `json:"parse_errors"`
	Stopped          bool    `json:"stopped"`
	ElapsedMS        float64 `json:"elapsed_ms"`
}

// ByteSource resolves sample payloads from an in-memory input.
type ByteSource map[string][]byte

// SampleBytes implements transform.PayloadSource.
func (b ByteSource) SampleBytes(s timeline.Sample) ([]byte, error) {
	data, ok := b[s.File]
	if !ok {
		return nil, fmt.Errorf("pipeline: unknown source %q", s.File)
	}
	if s.FileOffset < 0 || s.FileOffset+s.ByteLength > int64(len(data)) {
		return nil, fmt.Errorf("pipeline: sample [%d,%d) outside %s (%d bytes)", s.FileOffset, s.FileOffset+s.ByteLength, s.File, len(data))
	}
	return data[s.FileOffset : s.FileOffset+s.ByteLength], nil
}

// VerifyFiles re-reads exported artifacts and cross-checks them.
func VerifyFiles(manifestPath, framesPath string) (*verify.Report, error) {
	return verify.Files(verify.Options{ManifestPath: manifestPath, FramesPath: framesPath})
}

// Generate builds the injected sample stream for a config.
func Generate(cfg Config) (*fixture.InjectedStream, []Input, error) {
	kinds, err := fixture.ParseFaults(cfg.Faults)
	if err != nil {
		return nil, nil, err
	}
	injected, err := fixture.Inject(cfg.Seed, cfg.Tracks, cfg.Fragments, kinds)
	if err != nil {
		return nil, nil, err
	}
	inputs := []Input{{Name: "sample-41.fmp4", Data: injected.Data}}
	return injected, inputs, nil
}

// configForDigest zeroes fields that must not invalidate a resumable run:
// resume itself and the checkpoint path do not affect results.
func (c Config) configForDigest() Config {
	c.Resume = false
	c.CheckpointPath = ""
	return c
}

// Run executes the pipeline. The returned error is non-nil only for failures
// that cannot produce a result; recoverable input damage is recorded in Result.
func Run(cfg Config, injected *fixture.InjectedStream, inputs []Input) (*Result, error) {
	start := time.Now()
	res := &Result{Config: cfg}
	res.Inputs = inputs
	res.ConfigDigest = checkpoint.DigestConfig(cfg.configForDigest())
	res.TrackCount = cfg.Tracks
	parts := make([][]byte, 0, len(inputs))
	for _, in := range inputs {
		parts = append(parts, in.Data)
	}
	res.InputDigest = checkpoint.DigestInputs(parts...)

	store := &checkpoint.Store{Path: cfg.CheckpointPath}
	var st checkpoint.State
	var resumed *resumeState
	// Resume covers checkpoint validation plus restoring the ingest prefix, so
	// the reported cost reflects the work an interrupted run can actually skip.
	resumeStart := time.Now()
	if cfg.CheckpointPath != "" && store.Exists() && cfg.Resume {
		decision, err := checkpoint.Recover(store, checkpoint.Expect{
			InputDigest:    res.InputDigest,
			ConfigDigest:   res.ConfigDigest,
			VerifiedOutput: cfg.FramesOutput,
		})
		if err != nil {
			return nil, fmt.Errorf("resume refused: %w", err)
		}
		if decision.Resume {
			res.Resumed = true
			st = checkpoint.State{
				InputDigest:  res.InputDigest,
				ConfigDigest: res.ConfigDigest,
				LastStage:    decision.LastStage,
				Completed:    decision.Completed,
				ResumeAt:     decision.ResumeAt,
			}
			if raw, ok := decision.Payload[payloadIngest]; ok {
				var r resumeState
				if err := json.Unmarshal(raw, &r); err != nil {
					return nil, fmt.Errorf("resume refused: checkpoint ingest payload: %w", err)
				}
				if len(r.Fragments) > 0 && r.ResumeAt > 0 {
					resumed = &r
				}
			}
		}
	}

	if st.LastStage == "" {
		st = checkpoint.State{InputDigest: res.InputDigest, ConfigDigest: res.ConfigDigest}
	}

	lim := ingest.DefaultLimits()
	parsed, parseFaults, err := parseInputs(inputs, lim, resumed)
	if resumed != nil {
		res.ResumeMS = float64(time.Since(resumeStart).Microseconds()) / 1000
	}
	if err != nil {
		return nil, err
	}
	res.Init = parsed.inits
	res.Fragments = parsed.fragments
	// FragmentTotal (printed as fragments=) is the input's intended count, set
	// after generation so a truncated tail does not masquerade as a smaller
	// input. FragmentsSeen reports how many were actually parsed.
	res.FragmentsSeen = len(parsed.fragments)
	res.ReusedFragments = parsed.reused
	res.ParseErrors = len(parseFaults)
	res.ingestErrors = append(res.ingestErrors, parseFaults...)
	res.Stopped = parsed.stopped
	for _, e := range parseFaults {
		res.Errors = append(res.Errors, e)
	}
	st = checkpoint.Advance(st, checkpoint.StageIngest, parsed.resumeAt)
	if err := saveStage(store, st, res, parsed); err != nil {
		return nil, err
	}

	tracks, mapFaults := timeline.BuildTracks(parsed.inits, parsed.fragments, cfg.Timescale)
	for _, e := range mapFaults {
		res.Errors = append(res.Errors, e)
	}
	merged, mergeFaults := timeline.Merge(tracks, timeline.GapRecord)
	for _, e := range mergeFaults {
		res.Errors = append(res.Errors, e)
	}
	res.Gaps = merged.Gaps
	res.Duration = merged.Duration
	st = checkpoint.Advance(st, checkpoint.StageTimeline, parsed.resumeAt)
	if err := saveStage(store, st, res, parsed); err != nil {
		return nil, err
	}

	report, repaired := transform.Repair(tracks, transform.RepairOptions{TimelineTimescale: cfg.Timescale})
	res.Repair = report
	res.Repaired = repaired
	// Repair decisions are faults too: a duplicate or a regression parses
	// cleanly, so this is the only stage that can report it.
	for _, e := range report.Errors {
		res.Errors = append(res.Errors, e)
	}
	for _, d := range report.Discontinuities {
		res.Errors = append(res.Errors, fault.New(repairCode(d.Kind), d.File, d.Offset, "", d.Detail).WithFragment(d.Sequence))
	}
	// The synthesized timeline is the repaired, ordered sample sequence: that
	// is what frames are derived from and what verification checks. A track
	// that could not be ordered is reported but its already-ordered prefix is
	// still a valid, traceable part of the timeline.
	for _, rt := range repaired {
		res.Merged = append(res.Merged, rt.Samples...)
	}
	sort.SliceStable(res.Merged, func(i, j int) bool {
		a, b := res.Merged[i], res.Merged[j]
		if a.TimelineTime != b.TimelineTime {
			return a.TimelineTime < b.TimelineTime
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		if a.FragmentIndex != b.FragmentIndex {
			return a.FragmentIndex < b.FragmentIndex
		}
		return a.SampleIndex < b.SampleIndex
	})
	st = checkpoint.Advance(st, checkpoint.StageRepair, parsed.resumeAt)
	if err := saveStage(store, st, res, parsed); err != nil {
		return nil, err
	}

	source := ByteSource{}
	for _, in := range inputs {
		source[in.Name] = in.Data
	}
	frames, err := transform.BuildFrames(res.Merged, source, transform.FrameOptions{
		Seed:      cfg.Seed,
		Width:     cfg.FrameWidth,
		Height:    cfg.FrameHeight,
		Timescale: cfg.Timescale,
	})
	if err != nil {
		return nil, fmt.Errorf("frames: %w", err)
	}
	res.Frames = frames
	res.FrameCount = len(frames.Frames)
	st = checkpoint.Advance(st, checkpoint.StageFrames, parsed.resumeAt)
	if err := saveStage(store, st, res, parsed); err != nil {
		return nil, err
	}

	manifest := verify.NewManifest(cfg.Seed, res.ConfigDigest, res.InputDigest, sourceEntries(inputs, parsed))
	manifest.AddTracks(tracks)
	manifest.AddFrames(frames)
	manifest.SetTimelineDigest(res.Merged)
	for _, code := range orderedCodes(res.Errors) {
		manifest.FaultCodes = append(manifest.FaultCodes, string(code))
	}
	res.Manifest = manifest

	// Independent verification recomputes every invariant from recorded facts.
	failures := verify.Run(verify.Input{
		FileSizes:    fileSizes(inputs),
		TrackSamples: trackSamples(repaired),
		Merged:       res.Merged,
		Frames:       frames.Frames,
		FrameDigest:  frames.Digest,
		Manifest:     manifest,
	})
	res.Failures = failures
	for _, f := range failures {
		res.Errors = append(res.Errors, fault.New(fault.CodeInvariant, "", f.Offset, "", f.String()))
	}
	res.Faults = len(injected.Injections)
	res.FragmentTotal = injected.LogicalFragments
	for _, inj := range injected.Injections {
		res.InjectedOffsets = append(res.InjectedOffsets, inj.ByteOffset)
	}
	res.Recovered = true
	// Verification is the last stage; only a run that reaches this point with
	// no invariant failures records it, so a checkpoint always names a stage
	// whose output was actually validated.
	st = checkpoint.Advance(st, checkpoint.StageVerify, parsed.resumeAt)
	res.LastStage = st.LastStage
	if err := saveStage(store, st, res, parsed); err != nil {
		return nil, err
	}
	// The first error is the smallest offset at which the input proved bad,
	// whichever stage noticed it. This makes the reported position a property
	// of the input rather than of stage order.
	if first, ok := faultFirst(res.Errors); ok {
		res.FirstErrorOffset = first.Offset
		res.FirstErrorCode = first.Code
	}
	res.ElapsedMS = float64(time.Since(start).Microseconds()) / 1000
	return res, nil
}

// payloadIngest is the checkpoint payload key holding the parsed fragment
// prefix, so a resumed run does not rescan bytes it already validated.
const payloadIngest = "ingest_prefix"

// saveStage persists progress after each stage so an interruption leaves a
// reusable checkpoint. Save failures are reported, not ignored.
func saveStage(store *checkpoint.Store, st checkpoint.State, res *Result, parsed parsedInputs) error {
	if store.Path == "" {
		return nil
	}
	payload := map[string]json.RawMessage{}
	if res.Frames != nil {
		if data, err := json.Marshal(res.Frames.Digest); err == nil {
			payload["frame_digest"] = data
		}
	}
	prefix := resumeState{
		Fragments: parsed.fragments,
		ResumeAt:  parsed.resumeAt,
		Errors:    append([]*fault.Error(nil), res.ingestErrors...),
		Stopped:   parsed.stopped,
	}
	if data, err := json.Marshal(prefix); err == nil {
		payload[payloadIngest] = data
	}
	st.Fragments = len(parsed.fragments)
	st.Frames = res.FrameCount
	st.Payload = payload
	return store.Save(st)
}

// repairCode maps a repair decision to the shared fault vocabulary.
func repairCode(kind string) fault.Code {
	switch kind {
	case "duplicate":
		return fault.CodeDuplicateFragment
	case "tfdt_regression":
		return fault.CodeTfdtRegression
	case "sequence_gap":
		return fault.CodeSequenceGap
	case "unorderable":
		return fault.CodeUnmergeableTrack
	default:
		return fault.CodeInvariant
	}
}

func faultFirst(errs []*fault.Error) (*fault.Error, bool) {
	if len(errs) == 0 {
		return nil, false
	}
	first := errs[0]
	for _, e := range errs[1:] {
		if e.Offset < first.Offset {
			first = e
		}
	}
	return first, true
}

func orderedCodes(errs []*fault.Error) []fault.Code {
	set := make(map[fault.Code]struct{}, len(errs))
	for _, e := range errs {
		set[e.Code] = struct{}{}
	}
	codes := make([]fault.Code, 0, len(set))
	for c := range set {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}

func fileSizes(inputs []Input) map[string]int64 {
	out := make(map[string]int64, len(inputs))
	for _, in := range inputs {
		out[in.Name] = int64(len(in.Data))
	}
	return out
}

func trackSamples(repaired []*transform.RepairedTrack) map[uint32][]timeline.Sample {
	out := make(map[uint32][]timeline.Sample, len(repaired))
	for _, rt := range repaired {
		out[rt.ID] = rt.Samples
	}
	return out
}

func sourceEntries(inputs []Input, parsed parsedInputs) []verify.SourceEntry {
	fragmentsByFile := make(map[string]int)
	for _, f := range parsed.fragments {
		fragmentsByFile[f.File]++
	}
	out := make([]verify.SourceEntry, 0, len(inputs))
	for _, in := range inputs {
		sum := sha256.Sum256(in.Data)
		out = append(out, verify.SourceEntry{
			Name:      in.Name,
			Size:      int64(len(in.Data)),
			SHA256:    fmt.Sprintf("%x", sum[:]),
			Fragments: fragmentsByFile[in.Name],
		})
	}
	return out
}

// WriteOutputs writes frames as JSONL and the manifest as JSON. Frames are
// written through a temp file and renamed so a partial export is not left.
func (r *Result) WriteOutputs(framesPath, manifestPath string) error {
	if framesPath != "" {
		var lines []byte
		for _, f := range r.Frames.Frames {
			data, err := json.Marshal(f)
			if err != nil {
				return err
			}
			lines = append(lines, data...)
			lines = append(lines, '\n')
		}
		if err := atomicWrite(framesPath, lines); err != nil {
			return err
		}
	}
	if manifestPath != "" {
		if err := r.Manifest.Write(manifestPath); err != nil {
			return err
		}
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := dirOf(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".out-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}
