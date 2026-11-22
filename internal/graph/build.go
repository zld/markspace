package graph

import (
	"strconv"

	"mediacore/internal/core"
)

// TrackDependency is one declared prerequisite between source tracks.
type TrackDependency struct {
	Track     uint32
	DependsOn uint32
}

// Plan describes the requested processing shape. It is the single place where
// track presence, declared track dependencies, and parameters become graph
// nodes, so the pipeline never builds nodes ad hoc.
type Plan struct {
	HaveAudioTrack bool
	TrackDeps      []TrackDependency
	SampleRate     int
	Channels       string
	TargetKbps     int
	LoudnessLUFS   float64
	PeakDBFS       float64
}

// TrackNodeID names a source track's node.
func TrackNodeID(id uint32) NodeID { return NodeID("track-" + strconv.FormatUint(uint64(id), 10)) }

// ValidateTrackDeps checks declared dependencies for missing targets and
// cycles among source tracks alone, before any processing node is added.
func ValidateTrackDeps(deps []TrackDependency) error {
	known := map[uint32]bool{}
	for _, d := range deps {
		known[d.Track] = true
	}
	for _, d := range deps {
		if !known[d.DependsOn] {
			return core.New(core.CodeMissingTrack, core.StageGraph, false,
				"declared dependency names an unknown track").WithNode(string(TrackNodeID(d.Track)))
		}
	}
	nodes := make([]Node, 0, len(deps))
	seen := map[uint32]bool{}
	index := map[uint32]int{}
	for _, d := range deps {
		if !seen[d.Track] {
			seen[d.Track] = true
			index[d.Track] = len(nodes)
			nodes = append(nodes, Node{ID: TrackNodeID(d.Track)})
		}
	}
	for _, d := range deps {
		i := index[d.Track]
		nodes[i].Deps = append(nodes[i].Deps, TrackNodeID(d.DependsOn))
	}
	_, err := New(nodes)
	return err
}

// Build turns a plan into a validated graph. Missing required tracks and
// conflicting parameters are rejected here, before any media is processed.
func Build(p Plan) (*Graph, error) {
	if p.SampleRate <= 0 {
		return nil, core.New(core.CodeParamConflict, core.StageGraph, false,
			"output sample rate must be positive").WithNode(string(NodeResample))
	}
	if p.Channels == "" {
		return nil, core.New(core.CodeParamConflict, core.StageGraph, false,
			"output channel layout must be specified").WithNode(string(NodeLayout))
	}
	if p.TargetKbps <= 0 {
		return nil, core.New(core.CodeParamConflict, core.StageGraph, false,
			"target bitrate must be positive").WithNode(string(NodePacketize))
	}
	if p.PeakDBFS > 0 {
		return nil, core.New(core.CodeParamConflict, core.StageGraph, false,
			"peak ceiling must not exceed 0 dBFS").WithNode(string(NodeLoudness))
	}

	var nodes []Node
	nodes = append(nodes, Node{ID: NodeAudioTrack})
	nodes = append(nodes, Node{ID: NodeTimeline, Deps: []NodeID{NodeAudioTrack}})
	nodes = append(nodes, Node{ID: NodeDecodeParams, Deps: []NodeID{NodeAudioTrack},
		Params: map[string]string{"channels": p.Channels}})
	nodes = append(nodes, Node{ID: NodeResample, Deps: []NodeID{NodeTimeline, NodeDecodeParams},
		Params: map[string]string{"sample_rate": strconv.Itoa(p.SampleRate)}})
	nodes = append(nodes, Node{ID: NodeLayout, Deps: []NodeID{NodeResample},
		Params: map[string]string{"channels": p.Channels}})
	nodes = append(nodes, Node{ID: NodeLoudness, Deps: []NodeID{NodeLayout}})
	nodes = append(nodes, Node{ID: NodePacketize, Deps: []NodeID{NodeLoudness},
		Params: map[string]string{"target_kbps": strconv.Itoa(p.TargetKbps)}})

	if err := ValidateTrackDeps(p.TrackDeps); err != nil {
		return nil, err
	}

	g, err := New(nodes)
	if err != nil {
		return nil, err
	}
	if !p.HaveAudioTrack {
		return nil, core.New(core.CodeMissingTrack, core.StageGraph, false,
			"plan requires an audio track but the container has none").WithNode(string(NodeAudioTrack))
	}
	return g, nil
}

// MissingAudioTrackError is the stable error a caller can match on when a
// fixture has no usable AAC track.
func MissingAudioTrackError() error {
	return core.New(core.CodeMissingTrack, core.StageGraph, false, "audio track required").WithNode(string(NodeAudioTrack))
}
