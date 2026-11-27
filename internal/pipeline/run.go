package pipeline

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"

	"mediatimeline/asset"
	"mediatimeline/internal/synthetic"
	"mediatimeline/manifest"
)

// Run executes one complete offline processing pass and returns the summary.
// Independent valid intervals keep processing after per-asset failures.
func Run(cfg Config) (*Result, error) {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, err
	}
	res := &Result{Seed: cfg.Seed, AudioPeakDBFS: math.Inf(-1)}
	failFile, err := os.OpenFile(filepath.Join(cfg.OutDir, "errors.ndjson"),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	defer failFile.Close()
	recordFailure := func(f Failure) {
		res.Failures = append(res.Failures, f)
		failFile.Write(f.jsonLine())
	}

	// 1. Generate the deterministic dataset.
	lateIdx := lateEventIndex(cfg)
	fcfg := synthetic.FixtureConfig{
		Seed: cfg.Seed, ProgramKey: "demo", Segments: 10,
		Corrupt: cfg.Corrupt, CorruptIndex: 2,
		LateEvents: lateIdx, DriftRatePPM: cfg.DriftRatePPM, DriftOffsetNS: cfg.DriftOffsetNS,
	}
	paths, err := synthetic.Generate(cfg.OutDir, fcfg)
	if err != nil {
		recordFailure(Failure{Code: "GENERATE_FAILED", Stage: StageGenerate, Message: err.Error()})
		return res, err
	}

	// 2. Parse both manifest formats.
	hm, err := openManifest(paths.HLSManifest, manifest.ParseHLS, "demo")
	if err != nil {
		recordFailure(manifestFailure(err, StageManifest))
		return res, err
	}
	dm, err := openManifest(paths.DASHManifest, manifest.ParseDASH, "demo")
	if err != nil {
		recordFailure(manifestFailure(err, StageManifest))
		return res, err
	}
	res.ManifestSegments = countSegments(hm) + countSegments(dm)

	// 3. Stream-parse fragments and register every surviving version.
	am := asset.NewMap()
	truncOff := registerFragments(hm, dm, paths, am, recordFailure)
	_ = truncOff

	// 4. Merge versions: gaps and conflicts become stable findings.
	merged := asset.Merge(am)
	res.MergedIntervals = len(merged.Intervals)
	for _, is := range merged.Issues {
		switch is.Code {
		case asset.IssueGap:
			res.Gaps++
		case asset.IssueConflict:
			res.Conflicts++
		}
	}

	// 5. RTP, drift-corrected clock and watermark (separate function).
	if err := buildTimeline(cfg, paths, res, recordFailure); err != nil {
		return res, err
	}

	// 6. Evidence rendering over valid selected fragments.
	renderEvidence(cfg, merged, res, recordFailure)

	// 7. Journaled commits with rollback support.
	commitJournal(cfg, merged, res, recordFailure)

	writeSummary(cfg, res)
	return res, nil
}

func openManifest(path string,
	parse func(r io.Reader, pk, base string) (*manifest.Manifest, error),
	pk string) (*manifest.Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parse(f, pk, filepath.Dir(path))
}

func countSegments(m *manifest.Manifest) int {
	n := 0
	for _, t := range m.Tracks {
		n += len(t.Segments)
	}
	return n
}

func manifestFailure(err error, stage string) Failure {
	f := Failure{Stage: stage, Message: err.Error(), Code: string(manifest.ErrSyntax)}
	if me, ok := err.(*manifest.Error); ok {
		f.Code = string(me.Code)
		f.Asset = me.Asset
		f.Track = me.Track
		f.Line = me.Line
		f.ByteOffset = me.Offset
	}
	return f
}

func writeSummary(cfg Config, res *Result) {
	data, _ := json.MarshalIndent(res, "", "  ")
	_ = os.WriteFile(filepath.Join(cfg.OutDir, "summary.json"), data, 0o644)
}

// stableFrameHashSort keeps output ordering deterministic across runs.
func stableFrameHashSort(hashes []string) {
	sort.Strings(hashes)
}
