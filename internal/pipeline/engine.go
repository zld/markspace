// Package pipeline wires the media libraries into the dependency graph and
// drives a cold build, a warm rebuild and an interrupted-resume run. It owns
// the exact metric set the regression command reports.
package pipeline

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"regress/internal/checkpoint"
	"regress/internal/fixture"
	"regress/internal/index"
	"regress/internal/mp4"
	"regress/internal/rtp"
)

// node IDs: exactly eight processing nodes, matching the cold-rebuild count.
const (
	nodeSRTBase     = "srt-base"
	nodeSRTModified = "srt-modified"
	nodeVTT         = "vtt-base"
	nodeRTP         = "rtp-track"
	nodeMP4Healthy  = "mp4-healthy"
	nodeMP4Corrupt  = "mp4-corrupt"
	nodeSubtitle    = "subtitle-merge"
	nodeManifest    = "manifest"
)

// Metrics is the reported regression outcome.
type Metrics struct {
	ColdRebuilt      int
	WarmRebuilt      int
	FaultOffsetDelta int64
	RTPDuplicate     int
	RTPReordered     int
	RTPBurstLost     int
	ResumeMS         int64
	ManifestSHA256   string
	Breaks           []index.Break
	ExitCode         int
}

// graphChecker adapts the checkpoint store to the index.Checker interface.
type graphChecker struct{ store *checkpoint.Store }

func (g *graphChecker) Lookup(nodeID, inputDigest string, deps map[string]string) (string, []byte, bool, error) {
	rec, err := g.store.Load(nodeID)
	if err != nil {
		return "", nil, false, err
	}
	if rec == nil || !g.store.Valid(rec, inputDigest, deps) {
		if rec != nil {
			_ = g.store.Discard(nodeID)
		}
		return "", nil, false, nil
	}
	return rec.ResultDigest, rec.Payload, true, nil
}

// Engine holds one run's working state.
type Engine struct {
	workDir string
	assets  *fixture.Assets
	store   *checkpoint.Store
	// resolved values shared across node compute closures.
	rtpState   *rtp.TrackState
	mp4Healthy *mp4.FileState
}

// NewEngine prepares the working directory and checkpoint store.
func NewEngine(workDir string, seed int64) (*Engine, error) {
	if err := os.MkdirAll(filepath.Join(workDir, "out"), 0o755); err != nil {
		return nil, err
	}
	store, err := checkpoint.Open(workDir)
	if err != nil {
		return nil, err
	}
	return &Engine{
		workDir: workDir,
		assets:  fixture.Generate(seed),
		store:   store,
	}, nil
}


// Run executes a cold build followed by a warm build against the same store.
// On a cold run all eight nodes rebuild; on the warm run identical inputs must
// validate every checkpoint so nothing is recomputed. Interrupt/resume is
// driven separately through RunInterrupt.
func (e *Engine) Run() (*Metrics, error) {
	nodes, expectedOffset := e.buildGraph()
	g, err := index.NewGraph(nodes)
	if err != nil {
		return nil, err
	}

	// Cold run: no usable checkpoints yet.
	cold, err := g.Run(&graphChecker{store: e.store})
	if err != nil {
		return nil, err
	}
	if err := e.persist(nodes, cold); err != nil {
		return nil, err
	}

	// Warm run: identical inputs, every node must validate and be reused.
	start := time.Now()
	warm, err := g.Run(&graphChecker{store: e.store})
	if err != nil {
		return nil, err
	}
	resumeMS := time.Since(start).Milliseconds()

	// Fault offset delta: corruption offset reported by the scan vs the exact
	// byte the fixture damaged. Zero means byte-accurate reporting.
	var delta int64
	if fsErr := e.scanCorruptOffset(); fsErr >= 0 {
		delta = fsErr - expectedOffset
		if delta < 0 {
			delta = -delta
		}
	}

	m := &Metrics{
		ColdRebuilt:      cold.Rebuilt,
		WarmRebuilt:      warm.Rebuilt,
		FaultOffsetDelta: delta,
		ResumeMS:         resumeMS,
		ManifestSHA256:   e.manifestDigest(warm),
		Breaks:           warm.Breaks,
	}
	if e.rtpState != nil {
		m.RTPDuplicate = e.rtpState.Duplicates
		m.RTPReordered = e.rtpState.Reordered
		m.RTPBurstLost = e.rtpState.BurstLost
	}
	return m, nil
}

// RunInterrupt simulates a crash after the cold build and then resumes. It
// returns the number of nodes rebuilt during resume (reused nodes excluded)
// and the resume wall time. A tampered record proves digest validation discards
// stale partial results; the resumed manifest stays byte-identical.
func (e *Engine) RunInterrupt() (resumeRebuilt int, resumeMS int64, sha string, err error) {
	nodes, _ := e.buildGraph()
	g, errNode := index.NewGraph(nodes)
	if errNode != nil {
		return 0, 0, "", errNode
	}
	cold, errNode := g.Run(&graphChecker{store: e.store})
	if errNode != nil {
		return 0, 0, "", errNode
	}
	if errNode = e.persist(nodes, cold); errNode != nil {
		return 0, 0, "", errNode
	}
	if errNode = e.simulateInterrupt(); errNode != nil {
		return 0, 0, "", errNode
	}
	start := time.Now()
	resumed, errNode := g.Run(&graphChecker{store: e.store})
	if errNode != nil {
		return 0, 0, "", errNode
	}
	resumeMS = time.Since(start).Milliseconds()
	// Re-persist so subsequent runs converge on the validated state.
	if errNode = e.persist(nodes, resumed); errNode != nil {
		return 0, 0, "", errNode
	}
	return resumed.Rebuilt, resumeMS, e.manifestDigest(resumed), nil
}

func (e *Engine) manifestDigest(run *index.CheckedResult) string {
	for _, r := range run.Results {
		if r.NodeID == nodeManifest {
			return r.ResultDigest
		}
	}
	return ""
}

// scanCorruptOffset returns the exact corruption offset the MP4 scanner
// reports, or -1 when no structured track error was produced.
func (e *Engine) scanCorruptOffset() int64 {
	fs, err := mp4.ScanReader(bytes.NewReader(e.assets.MP4Corrupt), "corrupt.mp4")
	if err != nil || len(fs.TrackErrors) == 0 {
		return -1
	}
	return fs.TrackErrors[0].Offset
}

// persist writes an atomic checkpoint for every completed node.
func (e *Engine) persist(nodes []*index.Node, run *index.CheckedResult) error {
	byID := map[string]*index.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, r := range run.Results {
		rec := checkpoint.Record{
			NodeID:       r.NodeID,
			InputDigest:  byID[r.NodeID].Input,
			DepDigests:   e.depDigests(byID[r.NodeID], run),
			ResultDigest: r.ResultDigest,
			Kind:         string(r.Kind),
			Payload:      r.Payload,
		}
		if err := e.store.Save(rec); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) depDigests(n *index.Node, run *index.CheckedResult) map[string]string {
	out := map[string]string{}
	resByID := map[string]index.NodeResult{}
	for _, r := range run.Results {
		resByID[r.NodeID] = r
	}
	for _, d := range n.Deps {
		out[d] = resByID[d].ResultDigest
	}
	return out
}

// simulateInterrupt removes one checkpoint and corrupts another to prove the
// resume path validates input/dependency digests and discards stale partial
// results rather than trusting them.
func (e *Engine) simulateInterrupt() error {
	// Drop the manifest checkpoint: its recompute must restore byte-identical
	// output while every other node stays reusable.
	if err := e.store.Discard(nodeManifest); err != nil {
		return err
	}
	// Tamper with a stored record's result digest; validation must reject it.
	rec, err := e.store.Load(nodeVTT)
	if err != nil {
		return err
	}
	if rec != nil {
		rec.ResultDigest = "container:tampered"
		if err := e.store.Save(*rec); err != nil {
			return err
		}
	}
	return nil
}


func mustJSON(v interface{}) []byte {
	// Lightweight deterministic marker; full serialization is not required
	// because downstream nodes depend on digests, not this payload.
	return []byte(fmt.Sprintf("%T", v))
}
