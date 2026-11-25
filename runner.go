package mediakernel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// DemoOptions configures one end-to-end assetcheck run.
type DemoOptions struct {
	Seed            int64
	QueueCap        int
	SlowConsumerMs  int
	ManifestVersion int
	Corrupt         CorruptMode
	ExpectError     bool
	InterruptAfter  string // "", StageValidate, StageStage, StageCommit
	WorkDir         string
}

// RunResult is the top-level machine-readable document emitted by the CLI.
type RunResult struct {
	Command         string                `json:"command"`
	Seed            int64                 `json:"seed"`
	Counters        PipelineCounters      `json:"counters"`
	Timeline        TimelineReport        `json:"timeline"`
	ContentDigest   Digest                `json:"content_digest"`
	ContainerDigest Digest                `json:"container_digest"`
	IndexDigest     Digest                `json:"index_digest"`
	Plan            Plan                  `json:"plan"`
	Bitrate         BitrateSpec           `json:"bitrate"`
	Quality         QualityResult         `json:"quality"`
	Alignment       AlignmentSummary      `json:"alignment"`
	AlignmentItems  []SubtitleAlignment   `json:"alignment_items"`
	Corrupt         []CorruptPacketRecord `json:"corrupt_packets"`
	Migration       *MigrationReport      `json:"migration,omitempty"`
	SpecPass        bool                  `json:"spec_pass"`
	Failures        []string              `json:"spec_failures"`
	Error           *MediaError           `json:"error,omitempty"`
}

// ManifestFileName is the fixed on-disk manifest name inside WorkDir.
const ManifestFileName = "asset-manifest.json"

// RunDemo executes the full offline pipeline end to end and persists state in
// WorkDir. It returns a populated RunResult; processing errors are returned
// alongside a result so callers can still print quantified output.
func RunDemo(ctx context.Context, opts DemoOptions) (*RunResult, error) {
	if opts.WorkDir == "" {
		opts.WorkDir = "."
	}
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, NewError(PhaseMigrate, -1, "cannot create work dir").WithCause(err)
	}

	// 1. Generate the deterministic fixture (self-contained, no external media)
	//    and encapsulate it in a valid MP4 container. Parsing therefore goes
	//    through go-mp4 before the inner framed packet stream reaches the
	//    bounded pipeline.
	fixture, err := GenerateFixture(FixtureSpec{Seed: opts.Seed, Corrupt: opts.Corrupt})
	if err != nil {
		return nil, err
	}
	packetData := fixture.Data
	if opts.Corrupt == CorruptTruncated {
		packetData = TruncateFrame(packetData)
	}
	// ftyp minor version is deterministic per run and only changes encapsulation.
	containerBytes, err := WrapInMP4(packetData, 1)
	if err != nil {
		return nil, err
	}
	innerData, _, err := UnwrapMP4(containerBytes)
	if err != nil {
		// Container-level corruption still returns a structured offset/phase error.
		res := &RunResult{
			Command: "demo", Seed: opts.Seed,
			ContainerDigest: DigestBytes(containerBytes),
		}
		if me, ok := AsMediaError(err); ok {
			res.Error = me
		}
		return res, err
	}

	// 2. Stream-parse the unwrapped packet stream through the bounded pipeline
	//    with backpressure; the container digest covers the full MP4 bytes.
	cfg := RunConfig{
		QueueCap:          opts.QueueCap,
		SlowConsumerDelay: durationFromMs(opts.SlowConsumerMs),
		AlignmentTolMs:    25,
		// Documented delivery spec for the self-generated fixtures: a nominal
		// 24 kbps audio packet stream with a +/-20% allowed delivery band.
		TargetBitrateBps: FixtureTargetBitrateBps,
		BitrateTolerance: FixtureBitrateTolerance,
	}
	pipeline := NewPipeline(cfg)
	report, runErr := pipeline.ProcessWithContainer(ctx, innerData, containerBytes)

	result := &RunResult{
		Command:         "demo",
		Seed:            opts.Seed,
		Counters:        report.Counters,
		Timeline:        report.Timeline,
		ContentDigest:   report.ContentDigest,
		ContainerDigest: report.ContainerDigest,
		Bitrate:         report.Bitrate,
		Quality:         report.Quality,
		Corrupt:         report.Corrupt,
		Failures:        report.Failures,
	}

	// 3. On hard input errors, attach the structured error and stop before
	//    index/manifest work; the partial quantified report is still emitted.
	if runErr != nil {
		if me, ok := AsMediaError(runErr); ok {
			result.Error = me
		}
		return result, runErr
	}

	// 4. Incremental index against any prior generation persisted in WorkDir.
	prevStates := loadPriorStates(opts.WorkDir)
	states := []SourceState{{
		SourceID:        "fixture-source",
		ContentDigest:   report.ContentDigest,
		ContainerDigest: report.ContainerDigest,
	}}
	assets := buildAssetDependencies()
	subs := buildSubtitleSpecs(pipeline.clock)
	if err := pipeline.ApplySpecs(report, states, assets, subs, prevStates); err != nil {
		return nil, err
	}
	result.IndexDigest = report.IndexDigest
	result.Plan = report.Plan
	result.Alignment = report.Alignment
	result.AlignmentItems = report.AlignmentItems
	result.SpecPass = report.SpecPass
	result.Failures = report.Failures

	// 5. Persist the new source generation and run the staged migration.
	if err := persistStates(opts.WorkDir, states); err != nil {
		return nil, err
	}
	manifest := buildManifest(opts, states, report)
	migrator := NewMigrator(opts.WorkDir, ManifestFileName)
	if opts.InterruptAfter != "" {
		migrator.SetInterruptHook(func(stage string) error {
			if stage == opts.InterruptAfter {
				return errors.New("forced interrupt at " + stage)
			}
			return nil
		})
	}
	migReport, migErr := migrator.Run(manifest)
	result.Migration = migReport
	if migErr != nil {
		if me, ok := AsMediaError(migErr); ok {
			result.Error = me
		}
		return result, migErr
	}
	// Optionally leave a V1 manifest behind to exercise adaptation next run.
	if opts.ManifestVersion == ManifestV1 {
		if err := WriteLegacyV1(opts.WorkDir, manifest); err != nil {
			return result, err
		}
	}
	return result, nil
}

func buildAssetDependencies() []AssetDependency {
	return []AssetDependency{
		{AssetID: "dialogue-main", Kind: "dialogue", Sources: []string{"fixture-source"}, NeedsContainer: true},
		{AssetID: "subtitle-main", Kind: "subtitle", Sources: []string{"fixture-source"}, NeedsContainer: false},
		{AssetID: "sfx-cues", Kind: "sfx", Sources: []string{"fixture-source"}, NeedsContainer: true},
	}
}

// buildSubtitleSpecs creates deterministic subtitle intervals derived from
// the mapped timeline, both within the 25 ms alignment tolerance. The
// out-of-tolerance failure path is exercised directly in pipeline_test.go.
func buildSubtitleSpecs(clock Clock) []SubtitleSpec {
	base := int64(clock.effectiveRate() / 2) // 500 ms
	audio := TimeRange{StartTicks: base, EndTicks: base + clock.MillisToTicks(200)}
	first := TimeRange{StartTicks: audio.StartTicks + clock.MillisToTicks(2),
		EndTicks: audio.EndTicks + clock.MillisToTicks(2)} // 2 ms late
	second := TimeRange{StartTicks: audio.StartTicks - clock.MillisToTicks(10),
		EndTicks: audio.EndTicks - clock.MillisToTicks(10)} // 10 ms early
	return []SubtitleSpec{
		{ID: "sub-dialogue-a", Audio: audio, Subtitle: first},
		{ID: "sub-dialogue-b", Audio: audio, Subtitle: second},
	}
}

func buildManifest(opts DemoOptions, states []SourceState, report *Report) *Manifest {
	idxDigest := report.IndexDigest
	entries := make([]AssetEntry, 0, len(buildAssetDependencies()))
	for _, a := range buildAssetDependencies() {
		entries = append(entries, AssetEntry{
			AssetID:         a.AssetID,
			Kind:            a.Kind,
			SourceID:        a.Sources[0],
			ContentDigest:   states[0].ContentDigest,
			ContainerDigest: states[0].ContainerDigest,
			TimelineStart:   0,
			TimelineEnd:     report.Timeline.DurationTicks,
			Quality:         report.Quality,
		})
	}
	// The migrator always validates and commits the current schema. A request
	// for manifest V1 is handled by WriteLegacyV1 after commit so the next run
	// demonstrates DecodeManifest's V1->V2 adaptation.
	return &Manifest{
		Version:     ManifestCurrent,
		Seed:        opts.Seed,
		Stage:       StageValidate,
		IndexDigest: idxDigest,
		Entries:     entries,
	}
}

// WriteLegacyV1 overwrites the manifest with a genuine V1 document. A
// subsequent RunDemo/Verify decodes it via DecodeManifest and adapts to V2.
func WriteLegacyV1(workDir string, m *Manifest) error {
	legacy := LegacyManifestV1{Version: ManifestV1, Seed: m.Seed}
	for _, e := range m.Entries {
		legacy.Entries = append(legacy.Entries, LegacyManifestV1Entry{
			AssetID:       e.AssetID,
			Kind:          e.Kind,
			SourceID:      e.SourceID,
			SourceDigest:  e.ContentDigest,
			TimelineStart: e.TimelineStart,
			TimelineEnd:   e.TimelineEnd,
		})
	}
	raw, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		return NewError(PhaseMigrate, -1, "legacy marshal failed").WithCause(err)
	}
	tmp := filepath.Join(workDir, "."+ManifestFileName+".v1tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return NewError(PhaseMigrate, -1, "legacy write failed").WithCause(err)
	}
	return os.Rename(tmp, filepath.Join(workDir, ManifestFileName))
}

// --- persisted prior-generation helpers (small sidecar files) ---

const statesFileName = ".source-states.json"

func statesPath(workDir string) string { return filepath.Join(workDir, statesFileName) }

func persistStates(workDir string, states []SourceState) error {
	raw, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return NewError(PhaseDigest, -1, "states marshal failed").WithCause(err)
	}
	tmp := statesPath(workDir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return NewError(PhaseMigrate, -1, "states write failed").WithCause(err)
	}
	return os.Rename(tmp, statesPath(workDir))
}

func loadPriorStates(workDir string) []SourceState {
	raw, err := os.ReadFile(statesPath(workDir))
	if err != nil {
		return nil
	}
	var states []SourceState
	if json.Unmarshal(raw, &states) != nil {
		return nil
	}
	return states
}

func durationFromMs(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// VerifyResult captures one repeat verification and proves run-to-run
// stability of the manifest digest, index digest and affected-asset set.
type VerifyResult struct {
	Command         string   `json:"command"`
	Repeats         int      `json:"repeats"`
	ManifestDigest  Digest   `json:"manifest_digest"`
	IndexDigest     Digest   `json:"index_digest"`
	RecomputeAssets []string `json:"recompute_assets"`
	ReuseAssets     []string `json:"reuse_assets"`
	Stable          bool     `json:"stable"`
	SpecPass        bool     `json:"spec_pass"`
	AdaptedFromV1   bool     `json:"adapted_from_v1"`
}

// Verify reruns the pipeline repeat times against the persisted state and
// asserts identical digests and affected-asset sets on every repeat.
func Verify(ctx context.Context, workDir string, repeats int) (*VerifyResult, error) {
	if repeats < 1 {
		repeats = 1
	}
	// Detect a legacy V1 manifest and confirm DecodeManifest adapts it.
	adapted := false
	if raw, err := os.ReadFile(filepath.Join(workDir, ManifestFileName)); err == nil {
		var head struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(raw, &head) == nil && head.Version == ManifestV1 {
			adapted = true
		}
	}

	var prev *VerifyResult
	for i := 0; i < repeats; i++ {
		// Re-run the same seed-based demo; deterministic fixtures and sorted
		// planning make the digests and asset sets reproducible.
		seed := readSeed(workDir)
		res, err := RunDemo(ctx, DemoOptions{WorkDir: workDir, Seed: seed, QueueCap: 8})
		if err != nil {
			return nil, err
		}
		cur := &VerifyResult{
			Command:        "verify",
			Repeats:        repeats,
			ManifestDigest: *res.Migration.ManifestDigest,
			IndexDigest:    res.IndexDigest,
			Stable:         true,
			SpecPass:       res.SpecPass,
			AdaptedFromV1:  adapted,
		}
		for _, d := range res.Plan.Recompute {
			cur.RecomputeAssets = append(cur.RecomputeAssets, d.AssetID)
		}
		for _, d := range res.Plan.Reuse {
			cur.ReuseAssets = append(cur.ReuseAssets, d.AssetID)
		}
		if prev != nil {
			if !cur.ManifestDigest.Equal(prev.ManifestDigest) ||
				!cur.IndexDigest.Equal(prev.IndexDigest) ||
				!sameStringSet(cur.RecomputeAssets, prev.RecomputeAssets) ||
				!sameStringSet(cur.ReuseAssets, prev.ReuseAssets) {
				cur.Stable = false
				return cur, NewError(PhaseSpec, -1, "repeat run produced different digests or asset set")
			}
		}
		prev = cur
	}
	return prev, nil
}

func readSeed(workDir string) int64 {
	m, err := LoadManifest(filepath.Join(workDir, ManifestFileName))
	if err != nil {
		return 7
	}
	return m.Seed
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
