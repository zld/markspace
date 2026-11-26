package resume

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/atomicfile"
	"continuity/internal/manifest"
	"continuity/internal/media"
)

// ErrSimulatedInterrupt is returned when a test fixture asks the pipeline to
// stop after one durably committed cluster.
var ErrSimulatedInterrupt = errors.New("simulated interruption")

// Cluster is one deterministic resumable unit of media processing.
type Cluster struct {
	Index int    `json:"index"`
	Input []byte `json:"-"`
	Kind  string `json:"kind,omitempty"`
}

// TimelineIndex is the canonical sorted event index emitted on resume.
type TimelineIndex struct {
	SchemaVersion string               `json:"schema_version"`
	Events        []media.Event        `json:"events"`
	Report        media.TimelineReport `json:"report"`
}

// NewTimelineIndex sorts events and computes the stored quality report.
func NewTimelineIndex(events []media.Event) *TimelineIndex {
	sorted := media.SortEvents(events)
	return &TimelineIndex{
		SchemaVersion: manifest.CanonicalVersion,
		Events:        sorted,
		Report:        media.AnalyzeTimeline(sorted),
	}
}

// Pipeline runs deterministic cluster processing with checkpoints.
type Pipeline struct {
	Dir            string
	JobID          string
	Manifest       *manifest.Manifest
	Config         *manifest.ConfigPackage
	ConfigSHA256   string
	Timeline       *TimelineIndex
	Clusters       []Cluster
	InterruptAfter int // -1 disables; >=0 stops after this cluster commits
}

// Result describes a resume/replay execution.
type Result struct {
	ConfigSHA256 string `json:"config_sha256"`
	ConfigPath   string `json:"config_path"`
	TimelinePath string `json:"timeline_path"`
	OutputPath   string `json:"output_path"`
	OutputSHA256 string `json:"output_sha256"`
	Committed    []int  `json:"committed_clusters"`
	Skipped      []int  `json:"skipped_clusters"`
	Processed    []int  `json:"processed_clusters"`
	ClusterCount int    `json:"cluster_count"`
}

// Run verifies the checkpoint binding, skips valid committed clusters, and
// atomically publishes the combined output.
func (p *Pipeline) Run() (*Result, error) {
	if p.Config == nil || p.Manifest == nil {
		return nil, media.NewError(media.StagePipeline, -1, p.Dir, media.ErrMigration).WithKind("missing_config_or_manifest")
	}
	configSHA := p.ConfigSHA256
	if configSHA == "" {
		var err error
		configSHA, err = p.Config.SHA256()
		if err != nil {
			return nil, err
		}
	}

	existing, err := LoadCheckpoint(p.Dir)
	if err != nil {
		return nil, err
	}
	cp := existing
	prefix := NewPrefixHasher()
	if cp == nil {
		cp = &Checkpoint{JobID: p.JobID, Stage: "started", ClusterSHA256: map[int]string{}}
	} else {
		committedPrefix := len(cp.CommittedClusters)
		if committedPrefix > len(p.Clusters) {
			return nil, cp.VerifyBinding(Binding{InputPrefixSHA256: "mismatch", ManifestVersion: p.Manifest.SchemaVersion, ConfigSHA256: configSHA})
		}
		for i := 0; i < committedPrefix; i++ {
			if _, err := prefix.Write(p.Clusters[i].Input); err != nil {
				return nil, err
			}
		}
		expected := Binding{
			InputPrefixSHA256: SHA256BytesPrefixState(prefix),
			InputPrefixBytes:  prefix.Len(),
			ManifestVersion:   p.Manifest.SchemaVersion,
			ConfigSHA256:      configSHA,
		}
		if err := cp.VerifyBinding(expected); err != nil {
			return nil, err
		}
	}

	if err := os.MkdirAll(filepath.Join(p.Dir, "clusters"), 0o755); err != nil {
		return nil, err
	}

	result := &Result{ConfigSHA256: configSHA, ClusterCount: len(p.Clusters)}
	for i, cluster := range p.Clusters {
		path := p.clusterPath(i)
		if cp.IsClusterCommitted(i) && p.clusterOutputValid(cp, i, path) {
			result.Skipped = append(result.Skipped, i)
			continue
		}

		out := renderCluster(cluster, configSHA)
		if err := atomicfile.Write(path, out, 0o644); err != nil {
			return result, media.NewError(media.StagePipeline, clusterOffset(cluster), path, err).WithKind("cluster_write")
		}
		outSHA := SHA256Bytes(out)
		cp.MarkCluster(i, outSHA)
		if _, err := prefix.Write(cluster.Input); err != nil {
			return result, err
		}
		cp.InputPrefixSHA256 = prefix.Sum()
		cp.InputPrefixBytes = prefix.Len()
		cp.ManifestVersion = p.Manifest.SchemaVersion
		cp.ConfigSHA256 = configSHA
		cp.Stage = "cluster-" + fmt.Sprint(i)
		if err := cp.Save(p.Dir); err != nil {
			return result, err
		}
		result.Processed = append(result.Processed, i)
		result.Committed = append(result.Committed, i)

		if p.InterruptAfter >= 0 && p.InterruptAfter == i {
			return result, ErrSimulatedInterrupt
		}
	}

	configPath := filepath.Join(p.Dir, "config-package.json")
	configJSON, err := p.Config.CanonicalJSON()
	if err != nil {
		return result, err
	}
	if err := atomicfile.Write(configPath, configJSON, 0o644); err != nil {
		return result, err
	}
	result.ConfigPath = configPath

	timelinePath := filepath.Join(p.Dir, "timeline-index.json")
	timeline := p.Timeline
	if timeline == nil {
		timeline = NewTimelineIndex(nil)
	}
	timelineJSON, err := json.MarshalIndent(timeline, "", "  ")
	if err != nil {
		return result, err
	}
	if err := atomicfile.Write(timelinePath, timelineJSON, 0o644); err != nil {
		return result, err
	}
	result.TimelinePath = timelinePath

	finalBytes, err := p.assemble(cp)
	if err != nil {
		return result, err
	}
	outputPath := filepath.Join(p.Dir, "output.bin")
	if err := atomicfile.Write(outputPath, finalBytes, 0o644); err != nil {
		return result, err
	}
	result.OutputPath = outputPath
	result.OutputSHA256 = SHA256Bytes(finalBytes)
	cp.Stage = "complete"
	if err := cp.Save(p.Dir); err != nil {
		return result, err
	}
	return result, nil
}

// SHA256BytesPrefixState exposes the current incremental digest.
func SHA256BytesPrefixState(h *PrefixHasher) string { return h.Sum() }

func (p *Pipeline) clusterPath(index int) string {
	return filepath.Join(p.Dir, "clusters", fmt.Sprintf("cluster-%04d.bin", index))
}

func (p *Pipeline) clusterOutputValid(cp *Checkpoint, index int, path string) bool {
	want, ok := cp.ClusterSHA256[index]
	if !ok {
		return false
	}
	got, err := SHA256File(path)
	return err == nil && got == want
}

func clusterOffset(c Cluster) int64 { return int64(c.Index) }

func renderCluster(c Cluster, configSHA string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "CONTINUITY-CLUSTER-v1\n")
	fmt.Fprintf(&b, "cluster=%d\n", c.Index)
	if c.Kind != "" {
		fmt.Fprintf(&b, "kind=%s\n", c.Kind)
	}
	fmt.Fprintf(&b, "config_sha256=%s\n", configSHA)
	fmt.Fprintf(&b, "input_sha256=%s\n", SHA256Bytes(c.Input))
	b.WriteString("payload_bytes=" + fmt.Sprint(len(c.Input)) + "\n")
	b.Write(c.Input)
	b.WriteByte('\n')
	sum := SHA256Bytes(b.Bytes())
	fmt.Fprintf(&b, "cluster_sha256=%s\n", sum)
	return b.Bytes()
}

func (p *Pipeline) assemble(cp *Checkpoint) ([]byte, error) {
	var out bytes.Buffer
	fmt.Fprintf(&out, "CONTINUITY-OUTPUT-v1 clusters=%d config_sha256=%s\n", len(p.Clusters), cp.ConfigSHA256)
	for i := range p.Clusters {
		path := p.clusterPath(i)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, media.NewError(media.StagePipeline, int64(i), path, media.ErrTruncatedBlock).WithKind("missing_committed_cluster")
		}
		want := cp.ClusterSHA256[i]
		if want != "" && SHA256Bytes(data) != want {
			return nil, media.NewError(media.StagePipeline, int64(i), path, media.ErrCheckpointBinding).WithKind("cluster_checksum")
		}
		out.Write(data)
	}
	return out.Bytes(), nil
}
