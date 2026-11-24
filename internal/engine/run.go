// Package engine wires the subsystems into one resumable pipeline. It runs
// the explicit stage graph topologically, shares the 90 kHz clock across
// tracks, skips stages whose cache keys still match, blocks downstream stages
// when an upstream stage fails, and persists progress through atomic
// checkpoints so an interrupted process resumes from the last valid stage.
package engine

import (
	"fmt"
	"math"
	"path/filepath"
	"time"

	"mediapipe/internal/audio"
	"mediapipe/internal/graph"
	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
	"mediapipe/internal/store"
	"mediapipe/internal/verify"
)

// StageVersion changes invalidate every cached result of that stage when its
// implementation changes. Bump deliberately, never automatically.
const (
	versionVerify   = "verify-v1"
	versionColor    = "color-v1"
	versionCut      = "cut-v1"
	versionLayout   = "layout-v1"
	versionResample = "resample-v1"
	versionLoudness = "loudness-v1"
)

// silentLUFS is the finite value used in summaries and hashes for windows
// whose true loudness is -Inf. JSON cannot encode infinity, and a fixed
// sentinel keeps every derived hash deterministic.
const silentLUFS = -99.0

// finiteLUFS maps non-finite loudness onto the silent sentinel.
func finiteLUFS(v float64) float64 {
	if math.IsInf(v, -1) || math.IsNaN(v) {
		return silentLUFS
	}
	return v
}

// Config controls one pipeline run.
type Config struct {
	WorkDir        string
	ColorTarget    string
	TargetRate     int64
	TargetLUFS     float64
	TruePeak       float64
	InterruptAfter int // halt after this many newly executed stages; 0 = off
}

// Summary is the deterministic, hashable run output echoed by the CLI.
type Summary struct {
	TrackCount        int      `json:"track_count"`
	FrameCount        int      `json:"frame_count"`
	CorruptFirstFrame int      `json:"corrupt_first_frame"`
	CorruptOffset     int64    `json:"corrupt_offset_bytes"`
	ResumeMS          int64    `json:"resume_ms"`
	OutputSHA256      string   `json:"output_sha256"`
	LoudnessLUFS      float64  `json:"loudness_lufs"`
	ClipCount         int      `json:"clip_count"`
	ReusedStages      int      `json:"reused_stages"`
	ZeroPads          int      `json:"zero_pads"`
	BlockedStages     []string `json:"blocked_stages"`
}

// Engine holds mutable run state. One instance serves one pipeline execution.
type Engine struct {
	cfg           Config
	cp            *store.Checkpoint
	plan          *graph.Plan
	tracks        []*manifest.MappedTrack
	stageOrder    []string
	failed        map[string]bool
	blocked       map[string]bool
	digests       map[string]string
	attachedPCM   []float64
	audioOut      []float64
	windows       []audio.Window
	corruptFrame  int
	corruptOffset int64
	verifyReports []verify.Report
	reusedCount   int
	zeroPads      int
	metrics       map[string]map[string]float64
}

// stageSpec binds a graph node id to its executor and version. Executors
// return a content digest plus scalar metrics persisted with the checkpoint
// so resumed runs reproduce identical summaries without reprocessing.
type stageSpec struct {
	id      string
	trackID string
	version string
	params  interface{}
	inputs  []string // upstream node ids whose digests feed the cache key
	// rebuild marks stages whose executor must also run when the stage is
	// cache-skipped: audio stages reconstruct the in-memory PCM chain that a
	// fresh process would otherwise have no access to. The cached result stays
	// authoritative for caching; this only restores in-memory side effects.
	rebuild bool
	run     func(trackID string) (string, map[string]float64, error)
}

// New parses the manifest, verifies structural graph validity and prepares a
// run against any checkpoint already present in the work directory.
func New(cfg Config) (*Engine, error) {
	tracks, err := manifest.ParseFile(filepath.Join(cfg.WorkDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	cp, err := store.Load(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cfg:           cfg,
		cp:            cp,
		tracks:        tracks,
		failed:        map[string]bool{},
		blocked:       map[string]bool{},
		digests:       map[string]string{},
		metrics:       map[string]map[string]float64{},
		corruptFrame:  -1,
		corruptOffset: 0,
	}
	if err := e.buildPlan(); err != nil {
		return nil, err
	}
	return e, nil
}

// buildPlan constructs the explicit per-track stage graph:
// verify -> color -> cut (video) and verify -> layout -> resample -> loudness
// (audio).
func (e *Engine) buildPlan() error {
	nodes := make([]graph.Node, 0)
	for _, mt := range e.tracks {
		id := mt.Track.ID
		verifyID := id + "/verify"
		nodes = append(nodes, graph.Node{ID: verifyID, TrackID: id, Stage: "verify"})
		switch mt.Track.Kind {
		case model.KindVideo:
			colorID := id + "/color"
			cutID := id + "/cut"
			nodes = append(nodes,
				graph.Node{ID: colorID, TrackID: id, Stage: "color", Deps: []string{verifyID}},
				graph.Node{ID: cutID, TrackID: id, Stage: "cut", Deps: []string{colorID}},
			)
		case model.KindAudio:
			layoutID := id + "/layout"
			resampleID := id + "/resample"
			loudID := id + "/loudness"
			nodes = append(nodes,
				graph.Node{ID: layoutID, TrackID: id, Stage: "layout", Deps: []string{verifyID}},
				graph.Node{ID: resampleID, TrackID: id, Stage: "resample", Deps: []string{layoutID}},
				graph.Node{ID: loudID, TrackID: id, Stage: "loudness", Deps: []string{resampleID}},
			)
		}
	}
	p, err := graph.Build(nodes)
	if err != nil {
		return err
	}
	e.plan = p
	e.stageOrder = p.Order()
	return nil
}

// Run executes the graph. The returned error is non-nil only when at least
// one stage failed; independent stages still run and the summary reports
// precisely what was blocked.
func (e *Engine) Run(startedAt time.Time) (*Summary, error) {
	specs := e.specs()
	executed := 0
	reused := 0
	var firstErr error

	// Rehydrate digests and metrics of stages completed in a previous process.
	for id, st := range e.cp.Stages {
		if st.Status != "done" {
			continue
		}
		e.digests[id] = st.Digest
		if st.Metrics != nil {
			e.metrics[id] = st.Metrics
		}
	}
	e.rehydrateCorruption()

	for _, id := range e.stageOrder {
		spec := specs[id]
		if e.hasFailedDep(id) { // failure propagation blocks downstream
			e.blocked[id] = true
			continue
		}
		inputKey := e.inputKey(spec)
		paramsHash, err := store.ParamsHash(spec.params)
		if err != nil {
			return nil, err
		}
		cacheKey := store.CacheKey(spec.id, inputKey, paramsHash, spec.version)
		if st, ok := e.cp.Reusable(id, cacheKey); ok {
			e.digests[id] = st.Digest
			reused++
			// Reconstruct in-memory PCM side effects for skipped audio
			// stages so downstream stages and summaries match a clean run.
			if spec.rebuild {
				if _, _, rerr := spec.run(spec.trackID); rerr != nil {
					return nil, rerr
				}
			}
			continue
		}
		digest, metrics, err := spec.run(spec.trackID)
		if err != nil {
			e.failed[id] = true
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		executed++
		e.digests[id] = digest
		if metrics != nil {
			e.metrics[id] = metrics
		}
		if err := e.cp.MarkDone(e.cfg.WorkDir, store.StageState{
			StageID: id, CacheKey: cacheKey, InputKey: inputKey,
			ParamsHash: paramsHash, Version: spec.version, Digest: digest,
			Metrics: metrics,
		}); err != nil {
			return nil, model.NewError(model.ErrIO, "", 0, 0, err.Error())
		}
		if e.cfg.InterruptAfter > 0 && executed >= e.cfg.InterruptAfter {
			// Simulate a killed process: the checkpoint on disk is resumable.
			e.reusedCount = reused
			return nil, ErrInterrupted
		}
	}
	e.reusedCount = reused
	return e.summarize(startedAt, firstErr)
}

// ErrInterrupted marks the deliberately halted partial run; the checkpoint
// on disk is the resumable state.
var ErrInterrupted = fmt.Errorf("engine: interrupted after configured stage count")

func (e *Engine) hasFailedDep(id string) bool {
	node, ok := e.plan.Node(id)
	if !ok {
		return false
	}
	for _, d := range node.Deps {
		if e.failed[d] || e.blocked[d] {
			return true
		}
	}
	return false
}

func (e *Engine) inputKey(spec stageSpec) string {
	digests := make([]string, 0, len(spec.inputs))
	for _, in := range spec.inputs {
		digests = append(digests, e.digests[in])
	}
	return store.InputKey(digests)
}
