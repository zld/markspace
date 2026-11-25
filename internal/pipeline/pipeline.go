package pipeline

import (
	"os"
	"path/filepath"
	"strings"

	"medpipe/internal/fault"
	"medpipe/internal/fixture"
	"medpipe/internal/index"
	"medpipe/internal/ingest"
	"medpipe/internal/render"
	"medpipe/internal/timeline"
	"medpipe/internal/transform"
	"medpipe/internal/verify"
)

// Config controls one deterministic pipeline run.
type Config struct {
	Seed       int64
	BudgetMS   int64
	MaxBytes   int64
	MinQuality float64
	OutDir     string
	// CheckpointPath is optional; when set, state is gob-saved after each stage.
	CheckpointPath string
}

// DefaultConfig returns the acceptance-run defaults.
func DefaultConfig() Config {
	return Config{Seed: 17, BudgetMS: 5000, MaxBytes: 1 << 20, MinQuality: 99}
}

func (c Config) normalized() Config {
	if c.MinQuality == 0 {
		c.MinQuality = 99
	}
	return c
}

// State is the resumable stage state. All fields are gob-serializable.
type State struct {
	Cfg       Config
	Data      []byte
	Parse     *ingest.Result
	Roundtrip verify.RoundTripReport
	Merged    *timeline.Result
	Idx       *index.Index
	Windows   []index.Window
	MP4       *index.MP4Metadata
	Budget    *transform.Decision
	Manifest  *render.Manifest
	Hit       index.HitResult
	Summary   Summary
	Completed fault.Stage
}

var stageOrder = []fault.Stage{
	"generate",
	fault.StageParse,
	fault.StageTimeline,
	fault.StageIndex,
	fault.StageBudget,
	fault.StageRender,
	fault.StageVerify,
}

// Run executes every stage and persists checkpoints when configured.
func Run(cfg Config) (*RunResult, error) {
	st := &State{Cfg: cfg.normalized()}
	return execute(st)
}

// Resume loads path and reruns only stages after the last completed stage.
func Resume(path string) (*RunResult, error) {
	st, err := LoadCheckpoint(path)
	if err != nil {
		return nil, fault.New(fault.StageVerify, "resume_checkpoint", -1, -1, err)
	}
	st.Cfg = st.Cfg.normalized()
	return execute(st)
}

func execute(st *State) (*RunResult, error) {
	start := 0
	for i, name := range stageOrder {
		if name == st.Completed {
			start = i + 1
		}
	}
	for _, name := range stageOrder[start:] {
		var err error
		switch name {
		case "generate":
			err = stageGenerate(st)
		case fault.StageParse:
			err = stageParse(st)
		case fault.StageTimeline:
			err = stageMerge(st)
		case fault.StageIndex:
			err = stageIndex(st)
		case fault.StageBudget:
			err = stageBudget(st)
		case fault.StageRender:
			err = stageRender(st)
		case fault.StageVerify:
			err = stageVerify(st)
		}
		if err != nil {
			_ = SaveCheckpoint(st.Cfg.CheckpointPath, st)
			return st.Result(), err
		}
		st.Completed = name
		if err := SaveCheckpoint(st.Cfg.CheckpointPath, st); err != nil {
			return st.Result(), fault.New(fault.StageVerify, "write_checkpoint", -1, -1, err)
		}
	}
	return st.Result(), nil
}

func stageGenerate(st *State) error {
	fx, err := fixture.Generate(st.Cfg.Seed)
	if err != nil {
		return fault.New(fault.StageParse, "generate_fixture", -1, -1, err)
	}
	st.Data = fx.Data
	return nil
}

func stageParse(st *State) error {
	res, err := ingest.ParseBytes(st.Data, ingest.Options{})
	if err != nil {
		return fault.New(fault.StageParse, "parse_stream", -1, -1, err)
	}
	st.Parse = res
	return nil
}

func stageMerge(st *State) error {
	merged, err := timeline.Merge(st.Parse.Frames)
	if err != nil {
		return err
	}
	st.Merged = merged
	return nil
}

func stageIndex(st *State) error {
	frames := make([]*ingest.Frame, 0, len(st.Merged.Frames))
	for _, f := range st.Merged.Frames {
		frames = append(frames, f.Frame)
	}
	idx, err := index.Build(frames)
	if err != nil {
		return err
	}
	mp4Meta, err := index.PackageMP4Metadata(frames, idx)
	if err != nil {
		return err
	}
	st.Idx = idx
	st.MP4 = mp4Meta
	st.Windows = make([]index.Window, 0, len(idx.Keyframes))
	for _, k := range idx.Keyframes {
		st.Windows = append(st.Windows, index.Window{
			TrackID: k.TrackID, StartNS: k.TimestampNS, EndNS: k.TimestampNS + 1,
			ExpectHash: k.FragmentHash,
		})
	}
	return nil
}

func stageBudget(st *State) error {
	var pixelBytes int64
	for _, f := range st.Merged.Frames {
		pixelBytes += int64(len(f.Pixels))
	}
	options := transform.DefaultOptions(len(st.Merged.Frames), pixelBytes)
	decision, err := transform.Decide(options, transform.Constraints{
		MinQuality: st.Cfg.MinQuality, BudgetMS: st.Cfg.BudgetMS, MaxBytes: st.Cfg.MaxBytes,
	})
	if err != nil {
		return err
	}
	st.Budget = decision
	return nil
}

func stageRender(st *State) error {
	manifest, err := render.Export(st.Merged.Frames, st.Cfg.OutDir)
	if err != nil {
		return err
	}
	st.Manifest = manifest
	return nil
}

func stageVerify(st *State) error {
	fx, err := fixture.Generate(st.Cfg.Seed)
	if err != nil {
		return fault.New(fault.StageVerify, "regenerate_fixture", -1, -1, err)
	}
	rt, err := verify.CheckRoundTrip(fx.Canonical)
	if err != nil {
		return fault.New(fault.StageVerify, "roundtrip", -1, -1, err)
	}
	st.Roundtrip = rt
	if rt.Rate() < st.Cfg.MinQuality {
		return fault.New(fault.StageVerify, "roundtrip_quality_gate", -1, -1,
			&qualityError{msg: rt.Mismatches[0]})
	}

	hit := st.Idx.HitRate(st.Windows)
	st.Hit = hit
	if hit.Total == 0 || hit.Rate() != 100 {
		return fault.New(fault.StageVerify, "keyframe_hit_rate", -1, -1,
			&qualityError{msg: "legal sample keyframe windows did not all hit"})
	}

	s := Summary{
		RoundtripPercent:   rt.Rate(),
		KeyframeHitPercent: hit.Rate(),
		Duplicates:         st.Merged.Duplicates,
		Recovered:          st.Parse.Recovered,
		SegmentSHA256:      st.Manifest.SegmentSHA256Hex(),
	}
	for _, f := range st.Parse.Failures {
		switch {
		case f.Code == "checksum_mismatch":
			s.Corrupt++
		case strings.HasPrefix(f.Code, "truncated_"):
			s.Truncated++
		}
	}
	st.Summary = s
	if st.Cfg.OutDir != "" {
		_ = os.MkdirAll(st.Cfg.OutDir, 0o755)
		_ = writeJSON(filepath.Join(st.Cfg.OutDir, "failures.json"), st.Parse.Failures)
	}
	return nil
}

// Result builds the public result after execution.
func (st *State) Result() *RunResult {
	if st == nil {
		return &RunResult{}
	}
	var failures []fault.Failure
	if st.Parse != nil {
		failures = st.Parse.Failures
	}
	return &RunResult{
		Summary:        st.Summary,
		Failures:       failures,
		CheckpointPath: st.Cfg.CheckpointPath,
		OutputDir:      st.Cfg.OutDir,
	}
}

type qualityError struct{ msg string }

func (e *qualityError) Error() string { return e.msg }
