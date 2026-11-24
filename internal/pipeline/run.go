package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"archivecheck/internal/fixture"
	"archivecheck/internal/manifest"
	"archivecheck/internal/media"
	"archivecheck/internal/merr"
	"archivecheck/internal/quality"
	"archivecheck/internal/render"
	"archivecheck/internal/task"
	"archivecheck/internal/timeline"
)

// stateFile is the run-state artifact committed inside the output dir.
const stateFile = "run-state.json"

// RunOptions controls one end-to-end archive check.
type RunOptions struct {
	FixtureRoot string
	Format      string // manifest.FormatHLS / FormatDASH
	Window      int
	MaxAttempts int
	OutDir      string
	ResumeDir   string // previous output dir; empty means a fresh run
}

// RunResult is the terminal, JSON-serializable report.
type RunResult struct {
	Scenario          string                     `json:"scenario"`
	Status            string                     `json:"status"`
	TargetBitrateKbps int                        `json:"target_bitrate_kbps"`
	ActualBitrateKbps int                        `json:"actual_bitrate_kbps"`
	DeviationPct      float64                    `json:"deviation_pct"`
	MeanDeviationPct  float64                    `json:"mean_deviation_pct"`
	MaxDeviationPct   float64                    `json:"max_deviation_pct"`
	SampleCount       int                        `json:"sample_count"`
	DecodedFrames     int                        `json:"decoded_frames"`
	CorruptFrames     int                        `json:"corrupt_frames"`
	MeanLuma          int                        `json:"mean_luma"`
	ManifestAlignMs   float64                    `json:"manifest_alignment_ms"`
	FrameCount        int                        `json:"frame_count"`
	FrameHashes       []string                   `json:"frame_hashes"`
	LostPackets       int                        `json:"lost_packets"`
	DuplicatePackets  int                        `json:"duplicate_packets"`
	AffectedTracks    []uint32                   `json:"affected_tracks"`
	TruncatedSegments int                        `json:"truncated_segments"`
	RecoveredNodes    int                        `json:"recovered_nodes"`
	ExecutedNodes     int                        `json:"executed_nodes"`
	Blocking          *task.BlockingDep          `json:"blocking_dependency,omitempty"`
	NodeStates        map[string]*task.NodeState `json:"node_states"`
}

// runState is what resume reads back from the previous output directory.
type runState struct {
	Succeeded []string `json:"succeeded_nodes"`
	Scenario  string   `json:"scenario"`
}

// artifacts holds in-memory products produced during this run.
type artifacts struct {
	recovery map[uint32]map[uint32]*SegmentRecovery
	timeline *timeline.Timeline
	frames   *render.Manifest
	bitrate  quality.BitrateSummary
	stats    quality.FrameStats
}

// Run executes the full recovery pipeline and returns a report. A
// non-nil result may still describe a blocked run (exit code 1); only
// argument/setup failures return an error without a result.
func Run(opts RunOptions) (*RunResult, error) {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}
	if err := assertSafeOutput(opts.OutDir); err != nil {
		return nil, err
	}
	fx, specs, pl, err := loadInputs(opts)
	if err != nil {
		return nil, err
	}

	store, err := newNodeStore(opts.OutDir)
	if err != nil {
		return nil, err
	}
	var priorStore *nodeStore
	if opts.ResumeDir != "" {
		priorStore, err = newNodeStore(opts.ResumeDir)
		if err != nil {
			return nil, err
		}
		if _, err := readState(opts.ResumeDir); err != nil {
			return nil, err
		}
	}

	art := &artifacts{recovery: map[uint32]map[uint32]*SegmentRecovery{}}
	nodes, recoverKeys := buildNodes(opts, fx, specs, pl, art, store, priorStore)
	dag, err := task.New(nodes)
	if err != nil {
		return nil, err
	}

	runOpts := task.Options{MaxAttempts: opts.MaxAttempts}
	if priorStore != nil {
		var failed []string
		for _, key := range recoverKeys {
			if !priorStore.Has(key) {
				failed = append(failed, key)
			}
		}
		invalidated := dag.Invalidated(failed)
		runOpts.AlreadySucceeded = dag.ResumePlan(invalidated)
	}
	res := dag.Run(runOpts)

	out := &RunResult{
		Scenario:      fx.Scenario,
		NodeStates:    res.States,
		Blocking:      res.Blocking,
		ExecutedNodes: res.Executed,
	}
	if priorStore != nil {
		out.RecoveredNodes = res.Executed
	} else {
		out.RecoveredNodes = successfulRecoveryCount(recoverKeys, res)
	}
	fillReport(out, pl, art, res)

	if err := writeState(opts.OutDir, runState{
		Succeeded: successfulKeys(res), Scenario: fx.Scenario,
	}); err != nil {
		return nil, err
	}
	if res.Failed > 0 || res.Blocking != nil {
		out.Status = "blocked"
	} else if out.CorruptFrames > 0 || out.TruncatedSegments > 0 {
		out.Status = "degraded"
	} else {
		out.Status = "completed"
	}
	return out, nil
}

func successfulRecoveryCount(keys []string, res *task.Result) int {
	n := 0
	for _, k := range keys {
		if st := res.States[k]; st != nil && st.State == task.StatusSuccess {
			n++
		}
	}
	return n
}

func loadInputs(opts RunOptions) (*fixture.Fixture, []media.TrackSpec,
	*manifest.Playlist, error) {
	metaPath, err := fixture.AbsJoin(opts.FixtureRoot, fixture.MetaFile)
	if err != nil {
		return nil, nil, nil, err
	}
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, nil, nil, merr.Wrap(err, "pipeline", metaPath,
			"cannot read fixture metadata; generate fixtures first")
	}
	var meta struct {
		Scenario string `json:"scenario"`
		Window   int    `json:"window"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return nil, nil, nil, merr.Wrap(err, "pipeline", metaPath, "bad fixture metadata")
	}
	fx := &fixture.Fixture{
		Root: opts.FixtureRoot, Scenario: meta.Scenario,
		Window: meta.Window, TrackDir: map[uint32]string{},
	}
	manifestRel := fixture.HLSManifest
	parser := manifest.ParseHLS
	if opts.Format == manifest.FormatDASH {
		manifestRel = fixture.DASHManifest
		parser = manifest.ParseDASH
	}
	mp, err := fixture.AbsJoin(opts.FixtureRoot, manifestRel)
	if err != nil {
		return nil, nil, nil, err
	}
	mf, err := os.Open(mp)
	if err != nil {
		return nil, nil, nil, merr.Wrap(err, "pipeline", mp, "cannot open manifest")
	}
	defer mf.Close()
	pl, err := parser(mf)
	if err != nil {
		return nil, nil, nil, err
	}
	specs := make([]media.TrackSpec, 0, len(pl.Tracks))
	for _, t := range pl.Tracks {
		id := t.ID
		fx.TrackDir[id] = filepath.Join(opts.FixtureRoot, "track"+itoa(uint64(id)))
		specs = append(specs, media.TrackSpec{
			ID: id, Name: t.Name, Kind: t.Kind,
			Width: t.Width, Height: t.Height,
			TimebaseNum: fixture.FramesPerSegment, TimebaseDen: fixture.SegmentSeconds,
			TargetBitrateKbps: t.TargetBitrateBps / 1000,
		})
	}
	return fx, specs, pl, nil
}

func buildNodes(opts RunOptions, fx *fixture.Fixture, specs []media.TrackSpec,
	pl *manifest.Playlist, art *artifacts, store, priorStore *nodeStore) (
	[]*task.Node, []string) {

	var nodes []*task.Node
	var recoverKeys []string
	manifestKey := "manifest:parse"
	nodes = append(nodes, &task.Node{Key: manifestKey, Run: func() error { return nil }})

	for _, spec := range specs {
		for _, mt := range pl.Tracks {
			if mt.ID != spec.ID {
				continue
			}
			for _, ref := range mt.Refs {
				key := recoverKey(spec.ID, ref.Seq)
				recoverKeys = append(recoverKeys, key)
				spec, ref := spec, ref
				nodes = append(nodes, &task.Node{
					Key:  key,
					Deps: []string{manifestKey},
					Run: func() error {
						capPath := filepath.Join(fx.TrackDir[spec.ID],
							"seg-"+itoa(uint64(ref.Seq))+".rtp")
						segPath := filepath.Join(fx.TrackDir[spec.ID],
							"seg-"+itoa(uint64(ref.Seq))+".m4s")
						rec, err := recoverSegment(spec, ref.Seq, opts.Window, capPath, segPath)
						if err != nil {
							return err
						}
						// Keep the in-memory product for error reporting,
						// but a blocking failure must not commit an
						// artifact, so resume treats the key as invalid.
						remember(art, rec)
						if err := rec.blockingError(); err != nil {
							return err
						}
						if err := store.Put(key, rec); err != nil {
							return err
						}
						return nil
					},
				})
			}
		}
	}

	timelineKey := "timeline:compose"
	nodes = append(nodes, &task.Node{
		Key: timelineKey, Deps: recoverKeys,
		Run: func() error {
			recs, err := loadAllRecoveries(store, priorStore, recoverKeys)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				remember(art, rec)
			}
			return composeArtifacts(pl, specs, art)
		},
	})
	nodes = append(nodes, &task.Node{
		Key: "quality:stats", Deps: []string{timelineKey},
		Run: func() error {
			targets := map[uint32]int{}
			for _, t := range pl.Tracks {
				targets[t.ID] = t.TargetBitrateBps
			}
			_, art.bitrate = quality.MeasureBitrate(art.timeline.Segments, targets)
			art.stats = quality.CollectFrames(art.timeline.Segments)
			return nil
		},
	})
	nodes = append(nodes, &task.Node{
		Key: "render:frames", Deps: []string{timelineKey},
		Run: func() error {
			m, err := render.Export(art.timeline, opts.OutDir)
			if err != nil {
				return err
			}
			art.frames = m
			return nil
		},
	})
	return nodes, recoverKeys
}

func remember(art *artifacts, rec *SegmentRecovery) {
	if art.recovery[rec.Spec.ID] == nil {
		art.recovery[rec.Spec.ID] = map[uint32]*SegmentRecovery{}
	}
	art.recovery[rec.Spec.ID][rec.Seq] = rec
}

// loadAllRecoveries returns one artifact per recovery key, preferring
// freshly committed products and falling back to the prior run's store.
func loadAllRecoveries(store, priorStore *nodeStore, keys []string) (
	[]*SegmentRecovery, error) {
	out := make([]*SegmentRecovery, 0, len(keys))
	for _, key := range keys {
		var rec SegmentRecovery
		switch {
		case store.Has(key):
			if err := store.Get(key, &rec); err != nil {
				return nil, err
			}
		case priorStore != nil && priorStore.Has(key):
			if err := priorStore.Get(key, &rec); err != nil {
				return nil, err
			}
			// Re-commit into the new output so it stays self-contained.
			if err := store.Put(key, &rec); err != nil {
				return nil, err
			}
		default:
			return nil, merr.New(merr.KindDAG, "pipeline", key,
				"no committed recovery artifact found")
		}
		out = append(out, &rec)
	}
	return out, nil
}

func assertSafeOutput(outDir string) error {
	if outDir == "" {
		return merr.New(merr.KindArgument, "pipeline", "out",
			"output directory is required")
	}
	for _, part := range splitPath(outDir) {
		if part == ".." {
			return merr.New(merr.KindArgument, "pipeline", "out",
				"path traversal is not allowed in output directory")
		}
	}
	return nil
}

func splitPath(p string) []string {
	return splitAll(filepath.ToSlash(p), "/")
}

func splitAll(s, sep string) []string {
	var parts []string
	start := 0
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
