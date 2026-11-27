package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediarebuild/internal/checkpoint"
	"mediarebuild/internal/manifest"
	"mediarebuild/internal/model"
	"mediarebuild/internal/sample"
)

// ErrInterrupted is returned when a run stops early by request
// (--interrupt-after); completed stages remain checkpointed.
var ErrInterrupted = errors.New("pipeline: interrupted by request")

// Config controls a pipeline run.
type Config struct {
	Dir             string // output directory (samples, checkpoints, manifest)
	Seed            int64
	InterruptAfter  string // stop after this stage ("" = run to completion)
	RegenerateInput bool   // rewrite sample inputs before running
}

// Pipeline executes stages in order with checkpointed resume.
type Pipeline struct {
	cfg   Config
	store *checkpoint.Store
	Res   Results
	// LoadedStages lists stages restored from checkpoints (not recomputed).
	LoadedStages []string
	// RanStages lists stages computed in this run.
	RanStages []string
}

// New creates a pipeline, ensuring the output layout exists.
func New(cfg Config) (*Pipeline, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("pipeline: output directory required")
	}
	for _, d := range []string{cfg.Dir, filepath.Join(cfg.Dir, "samples")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	store, err := checkpoint.NewStore(filepath.Join(cfg.Dir, "checkpoints"))
	if err != nil {
		return nil, err
	}
	return &Pipeline{cfg: cfg, store: store}, nil
}

// SampleDir returns the directory holding generated inputs.
func (p *Pipeline) SampleDir() string { return filepath.Join(p.cfg.Dir, "samples") }

// GenerateInputs (re)writes all sample inputs deterministically.
func (p *Pipeline) GenerateInputs() error {
	return generateInputs(p.SampleDir(), p.cfg.Seed)
}

// Run executes stages in order, skipping any with a valid checkpoint.
func (p *Pipeline) Run(ctx context.Context) error {
	if p.cfg.RegenerateInput {
		if err := p.GenerateInputs(); err != nil {
			return err
		}
	}
	for _, stage := range StageOrder {
		if err := ctx.Err(); err != nil {
			return err // keep completed checkpoints; stop reading
		}
		if p.store.Completed(stage) {
			if err := p.load(stage); err != nil {
				return err
			}
			p.LoadedStages = append(p.LoadedStages, stage)
		} else {
			if err := p.compute(ctx, stage); err != nil {
				return err
			}
			p.RanStages = append(p.RanStages, stage)
		}
		if stage == p.cfg.InterruptAfter {
			return ErrInterrupted
		}
	}
	return nil
}

// load restores a completed stage's result from its checkpoint.
func (p *Pipeline) load(stage string) error {
	var err error
	switch stage {
	case StageIngest:
		p.Res.Ingest = &IngestResult{}
		err = p.loadInto(stage, p.Res.Ingest)
	case StageRTP:
		p.Res.RTP = &RTPResult{}
		err = p.loadInto(stage, p.Res.RTP)
	case StageRTCP:
		p.Res.RTCP = &RTCPResult{}
		err = p.loadInto(stage, p.Res.RTCP)
	case StageScan:
		p.Res.Scan = &ScanResult{}
		err = p.loadInto(stage, p.Res.Scan)
	case StageValidate:
		p.Res.Validate = &ValidateResult{}
		err = p.loadInto(stage, p.Res.Validate)
	case StageFragment:
		p.Res.Fragment = &FragmentResult{}
		err = p.loadInto(stage, p.Res.Fragment)
	case StageMedia:
		p.Res.Media = &MediaResult{}
		err = p.loadInto(stage, p.Res.Media)
	case StageMerge:
		p.Res.Merge = &MergeResult{}
		err = p.loadInto(stage, p.Res.Merge)
	}
	return err
}

func (p *Pipeline) loadInto(stage string, v any) error {
	ok, err := p.store.Load(stage, v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("pipeline: checkpoint for stage %q vanished", stage)
	}
	return nil
}

// compute runs one stage and checkpoints its result.
func (p *Pipeline) compute(ctx context.Context, stage string) error {
	var state any
	var err error
	dir := p.SampleDir()
	switch stage {
	case StageIngest:
		p.Res.Ingest, err = runIngest(ctx, dir)
		state = p.Res.Ingest
	case StageRTP:
		p.Res.RTP, err = runRTP(ctx, dir)
		state = p.Res.RTP
	case StageRTCP:
		p.Res.RTCP, err = runRTCP(ctx, dir)
		state = p.Res.RTCP
	case StageScan:
		p.Res.Scan, err = runScan(ctx, dir)
		state = p.Res.Scan
	case StageValidate:
		p.Res.Validate, err = runValidate(ctx, dir)
		state = p.Res.Validate
	case StageFragment:
		p.Res.Fragment, err = runFragment(ctx, dir)
		state = p.Res.Fragment
	case StageMedia:
		p.Res.Media, err = runMedia(ctx, dir, p.cfg.Seed)
		state = p.Res.Media
	case StageMerge:
		p.Res.Merge, err = p.runMerge()
		state = p.Res.Merge
	default:
		err = fmt.Errorf("pipeline: unknown stage %q", stage)
	}
	if err != nil {
		return err
	}
	// The stage is only marked complete once its state is persisted.
	return p.store.Save(stage, state)
}

// runMerge folds all stage results into the deterministic manifest.
func (p *Pipeline) runMerge() (*MergeResult, error) {
	m := manifest.NewMerger()
	for _, s := range p.Res.RTP.Streams {
		m.Add(manifest.Entry{
			Key:  "rtp:" + s.Name,
			Kind: "rtp",
			Metrics: map[string]uint64{
				"packets": uint64(s.Packets),
				"unique":  uint64(s.Unique),
				"dups":    uint64(s.Dups),
				"lost":    uint64(s.Lost),
			},
		})
	}
	for _, name := range containerFiles {
		var diags model.Diags
		diags = append(diags, p.Res.Scan.Files[name].Diags...)
		diags = append(diags, p.Res.Validate.Files[name].Diags...)
		if fr, ok := p.Res.Fragment.Files[name]; ok {
			diags = append(diags, fr.Diags...)
		}
		m.Add(manifest.Entry{Key: "mp4:" + name, Kind: "container", Diagnostics: diags})
	}
	m.Add(manifest.Entry{
		Key:  "media:video",
		Kind: "media",
		Metrics: map[string]uint64{
			"frames":           uint64(len(p.Res.Media.FrameHashes)),
			"pixel_mean_milli": uint64(p.Res.Media.PixelMean * 1000),
			"pixel_peak":       uint64(p.Res.Media.PixelPeak),
			"audio_peak":       uint64(p.Res.Media.AudioPeak),
			"align_error_ns":   uint64(p.Res.RTCP.AlignmentErrorNanos),
		},
		Hashes: map[string]string{
			"fragment_sha256": p.Res.Media.FragmentHash,
			"frame0_sha256":   p.Res.Media.FrameHashes[0],
		},
	})
	man, sum := m.Finalize()
	if err := writeAtomic(filepath.Join(p.cfg.Dir, "manifest.json"), mustJSON(man)); err != nil {
		return nil, err
	}
	return &MergeResult{
		ManifestSHA256: fmt.Sprintf("%x", sum),
		Entries:        len(man.Entries),
	}, nil
}

// writeAtomic writes data to a temp file in the same directory and
// renames it into place.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// generateInputs writes all deterministic sample inputs.
func generateInputs(dir string, seed int64) error {
	clean := sample.GenRTPStream(seed, 40, false)
	lossy := sample.GenRTPStream(seed, 40, true)
	files := map[string][]byte{
		"rtp-clean.bin": clean.Records,
		"rtp-lossy.bin": lossy.Records,
		"rtcp.bin":      sample.GenRTCPRecords(),
	}
	// Media payloads: framed video samples.
	var samples [][]byte
	for _, f := range sample.Frames(seed) {
		samples = append(samples, sample.FrameSample(f))
	}
	files["normal.mp4"] = sample.BuildNormalMP4(sample.VideoTimescale, sample.VideoFrameDuration, samples)
	frags := [][][]byte{samples[:15], samples[15:]}
	files["clean-frag.mp4"] = sample.BuildFragmentedMP4(sample.VideoTimescale, sample.VideoFrameDuration, frags)
	files["truncated.mp4"] = sample.BuildTruncatedMP4(
		sample.VideoTimescale, sample.VideoFrameDuration, samples,
		len(files["normal.mp4"])*7/10)
	corrupt, _ := sample.CorruptSampleStart(files["clean-frag.mp4"], 1, 3)
	files["corrupt-frag.mp4"] = corrupt
	files["incomplete-index.mp4"] = sample.BuildIncompleteIndexMP4(
		sample.VideoTimescale, sample.VideoFrameDuration, samples)
	for name, data := range files {
		if err := writeAtomic(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}
