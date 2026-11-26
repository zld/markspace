// Package pipeline wires ingest, quality planning, the DAG scheduler,
// checkpointing and the provenance manifest into one resumable run. All
// data flows between nodes through committed artifacts, so a resumed run
// reconstructs identical state without re-executing reused nodes.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"pwf/internal/checkpoint"
	"pwf/internal/dag"
	"pwf/internal/media"
	"pwf/internal/provenance"
	"pwf/internal/quality"
)

// SourceKind distinguishes the two supported local input formats.
type SourceKind string

const (
	KindRTP SourceKind = "rtp"
	KindMP4 SourceKind = "mp4"
)

// Source is one track's input descriptor.
type Source struct {
	Track   string
	Kind    SourceKind
	Path    string
	Codec   string
	ClockHz uint32 // RTP only
}

// Options controls a run.
type Options struct {
	WorkDir      string
	Sources      []Source
	Thresholds   quality.Thresholds
	Workers      int
	StopAfterKey string // interruption simulation; empty means a full run
}

// Outcome is the structured result consumed by the CLI.
type Outcome struct {
	Measurements []*quality.Measurement
	Plan         *quality.Plan
	Results      []dag.RunResult
	Manifest     *provenance.Manifest
	ReusedNodes  int
	ElapsedMS    int64
	Interrupted  bool
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Run executes (or resumes) the full pipeline.
func Run(opt Options) (*Outcome, error) {
	started := time.Now()
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	store, err := checkpoint.Open(opt.WorkDir)
	if err != nil {
		return nil, err
	}

	sources := normalizeSources(opt.Sources)
	var nodes []dag.Node
	ingestKeys := map[string]string{}
	measureKeys := map[string]string{}
	measureDeps := make([]string, 0, len(sources))

	for _, src := range sources {
		inputDigest, err := fileDigest(src.Path)
		if err != nil {
			return nil, err
		}
		ingestCfg := "ingest:" + string(src.Kind) + ":" + src.Codec
		ingestKey := dag.NodeKey(inputDigest, ingestCfg)
		ingestKeys[src.Track] = ingestKey
		store.RecordDigests(ingestKey, inputDigest, ingestCfg)
		nodes = append(nodes, dag.Node{
			Key: ingestKey, InputDigest: inputDigest, ConfigDigest: ingestCfg,
			Run: func(rc *dag.RunContext) ([]byte, error) { return runIngest(src) },
		})

		measureKey := dag.NodeKey(ingestKey, "measure")
		measureKeys[src.Track] = measureKey
		measureDeps = append(measureDeps, measureKey)
		store.RecordDigests(measureKey, ingestKey, "measure")
		nodes = append(nodes, dag.Node{
			Key: measureKey, InputDigest: ingestKey, ConfigDigest: "measure",
			Deps: []string{ingestKey},
			Run: func(rc *dag.RunContext) ([]byte, error) {
				raw, ok := rc.Output(ingestKey)
				if !ok {
					return nil, media.NewError(media.KindCheckpoint, media.StagePipeline,
						"ingest output unavailable", nil).WithTrack(src.Track)
				}
				art, err := decodeArtifact(raw)
				if err != nil {
					return nil, err
				}
				return json.MarshalIndent(art.Measurement, "", "  ")
			},
		})
	}

	planCfg := "plan:" + hashBytes([]byte(fmt.Sprintf("%+v", opt.Thresholds)))
	planInput := sortJoin(measureDeps)
	planKey := dag.NodeKey(planInput, planCfg)
	store.RecordDigests(planKey, planInput, planCfg)
	nodes = append(nodes, dag.Node{
		Key: planKey, InputDigest: planInput, ConfigDigest: planCfg, Deps: measureDeps,
		Run: func(rc *dag.RunContext) ([]byte, error) {
			measurements := make([]*quality.Measurement, 0, len(sources))
			for _, src := range sources {
				raw, ok := rc.Output(measureKeys[src.Track])
				if !ok {
					return nil, media.NewError(media.KindCheckpoint, media.StagePlan,
						"measurement output unavailable", nil).WithTrack(src.Track)
				}
				var m quality.Measurement
				if err := json.Unmarshal(raw, &m); err != nil {
					return nil, media.NewError(media.KindCheckpoint, media.StagePlan,
						"measurement artifact malformed", err).WithTrack(src.Track)
				}
				measurements = append(measurements, &m)
			}
			plan, perr := quality.PlanTracks(measurements, opt.Thresholds)
			if perr != nil {
				return nil, perr
			}
			return []byte(plan.Summary()), nil
		},
	})

	manifestCfg := "manifest:" + provenanceSchema
	manifestKey := dag.NodeKey(planKey, manifestCfg)
	store.RecordDigests(manifestKey, planKey, manifestCfg)
	nodes = append(nodes, dag.Node{
		Key: manifestKey, InputDigest: planKey, ConfigDigest: manifestCfg,
		Deps: append(append([]string{planKey}, measureDeps...), ingestKeysValues(ingestKeys, sources)...),
		Run: func(rc *dag.RunContext) ([]byte, error) {
			b := provenance.NewBuilder()
			for _, src := range sources {
				if err := b.AddSource(src.Track, src.Path, src.Codec); err != nil {
					return nil, err
				}
				raw, ok := rc.Output(ingestKeys[src.Track])
				if !ok {
					return nil, media.NewError(media.KindCheckpoint, media.StageProvenance,
						"ingest artifact unavailable", nil).WithTrack(src.Track)
				}
				art, err := decodeArtifact(raw)
				if err != nil {
					return nil, err
				}
				if art.RTP != nil {
					b.AddRTPStream(art.RTP)
				}
				if art.MP4 != nil {
					b.AddMP4Track(art.MP4)
				}
			}
			if report, ok := rc.Output(planKey); ok {
				b.SetConfigReport(string(report))
			}
			m, err := b.Build()
			if err != nil {
				return nil, err
			}
			if _, err := m.Write(filepath.Join(opt.WorkDir, "manifest.json")); err != nil {
				return nil, err
			}
			return []byte(m.RunSHA256), nil
		},
	})

	graph, err := dag.NewGraph(nodes)
	if err != nil {
		return nil, err
	}
	exec := &dag.Executor{
		Graph: graph, Workers: opt.Workers, Commit: store,
		Skip: func(key string) (string, bool) {
			n := graph.Node(key)
			if n == nil {
				return "", false
			}
			return store.Reusable(key, n.InputDigest, n.ConfigDigest)
		},
		StopAfterKey: opt.StopAfterKey,
	}
	results, runErr := exec.Execute()

	out := &Outcome{Results: results, ElapsedMS: time.Since(started).Milliseconds()}
	for _, r := range results {
		if r.Reused {
			out.ReusedNodes++
		}
	}
	if be, ok := runErr.(*dag.BlockedError); ok && be.Interrupted {
		out.Interrupted = true
		return out, nil
	}
	if runErr != nil {
		return out, runErr
	}

	// Full run: reconstruct measurements and plan for the CLI report.
	out.Measurements = make([]*quality.Measurement, 0, len(sources))
	for _, src := range sources {
		raw, err := store.Read(measureKeys[src.Track])
		if err != nil {
			return out, err
		}
		var m quality.Measurement
		if err := json.Unmarshal(raw, &m); err != nil {
			return out, err
		}
		out.Measurements = append(out.Measurements, &m)
	}
	plan, planErr := quality.PlanTracks(out.Measurements, opt.Thresholds)
	if planErr != nil {
		return out, planErr
	}
	out.Plan = plan
	manifestRaw, err := store.Read(manifestKey)
	if err != nil {
		return out, err
	}
	out.Manifest = &provenance.Manifest{RunSHA256: string(manifestRaw)}
	return out, nil
}

func normalizeSources(in []Source) []Source {
	out := append([]Source(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Track < out[j].Track })
	return out
}

func sortJoin(keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	out := ""
	for i, k := range sorted {
		if i > 0 {
			out += "|"
		}
		out += k
	}
	return out
}

func ingestKeysValues(m map[string]string, sources []Source) []string {
	out := make([]string, 0, len(m))
	for _, s := range sources {
		out = append(out, m[s.Track])
	}
	return out
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", media.NewError(media.KindCorrupt, media.StagePipeline,
			"cannot open source for digest", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", media.NewError(media.KindCorrupt, media.StagePipeline,
			"failed hashing source", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
