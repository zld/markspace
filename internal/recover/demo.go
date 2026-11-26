package recover

import (
	"bytes"
	"os"

	"mediapack/internal/commit"
	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/safepath"
	"mediapack/internal/sample"
)

// Options configures a demo run.
type Options struct {
	WorkDir           string
	Rates             int
	CrashBeforeCommit bool
	Config            sample.Config
}

// Demo generates the self-generated TS sample, analyses/plans it and writes
// all fragments through the crash-safe protocol. With CrashBeforeCommit it
// returns commit.ErrSimulatedCrash after exactly one prepared (not committed)
// artifact, which the CLI maps to exit code 20.
func Demo(o Options) (*manifest.Manifest, error) {
	work, err := safepath.ValidateLocalOutput(o.WorkDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	rates := o.Rates
	if rates <= 0 {
		rates = 2
	}
	cfg := o.Config
	if cfg.Intervals == 0 {
		cfg = sample.DefaultConfig()
	}

	layout := sample.Generate(cfg)
	srcRel := "source.ts"
	srcFull, err := manifest.Resolve(work, srcRel)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(srcFull, layout.Data, 0o644); err != nil {
		return nil, err
	}
	src, err := os.Open(srcFull)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return nil, err
	}

	analysis, err := fanout.Analyze(src, st.Size())
	if err != nil {
		return nil, err
	}
	profiles := fanout.Profiles(rates)
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}

	m := manifest.New(srcRel, rates, names)
	m.SourceLength = st.Size()
	m.SourceSHA256, _, _ = manifest.HashFile(srcFull)
	m.FullPackets = analysis.FullPackets
	m.Issues = analysis.Issues
	for _, iv := range analysis.Intervals {
		m.Intervals = append(m.Intervals, manifest.IntervalRecord{
			Index: iv.Index, ID: iv.ID, KeyframeOffset: iv.KeyframeOffset,
			StartOffset: iv.StartOffset, EndOffset: iv.EndOffset,
			StartTick: iv.StartTick, EndTick: iv.EndTick,
			StructuralDigest: iv.StructuralDigest, MappingPaused: iv.MappingPaused,
			MappingRecovered: iv.MappingRecovered, PauseReason: iv.PauseReason,
		})
		if iv.Blocked {
			continue // incomplete mapping that never resolved: do not package
		}
		for _, p := range profiles {
			m.Artifacts = append(m.Artifacts, manifest.Artifact{
				ID: fanout.ArtifactID(p.Index, iv.ID), Kind: "segment",
				IntervalIndex: iv.Index, RateIndex: p.Index, RateName: p.Name,
				RelPath:     fanout.RelPath(p.Index, iv.ID),
				SourceStart: iv.StartOffset, SourceEnd: iv.EndOffset,
				StructuralDigest: iv.StructuralDigest, StartTick: iv.StartTick,
				EndTick: iv.EndTick, KeyframeOffset: iv.KeyframeOffset,
				Status: manifest.StatusPending, MappingPaused: iv.MappingPaused,
				MappingRecovered: iv.MappingRecovered,
			})
		}
	}
	if err := m.Save(work); err != nil {
		return m, err
	}

	j, err := commit.OpenJournal(work)
	if err != nil {
		return m, err
	}
	defer j.Close()

	for i := range m.Artifacts {
		art := &m.Artifacts[i]
		data, err := FragmentBytes(src, art)
		if err != nil {
			return m, err
		}
		if err := commit.Prepare(work, m, j, art, bytes.NewReader(data)); err != nil {
			return m, err
		}
		if o.CrashBeforeCommit && i == 0 {
			return m, commit.ErrSimulatedCrash
		}
		if _, _, err := commit.Commit(work, m, j, art.ID); err != nil {
			return m, err
		}
	}
	if rep, err := evaluate(work, m); err == nil {
		m.FanInAligned = rep.Aligned
		m.BoundaryErrorPackets = rep.BoundaryPackets
		_ = m.Save(work)
	}
	return m, nil
}
