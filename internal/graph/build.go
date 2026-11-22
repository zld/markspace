package graph

import (
	"strconv"

	"mediacore/internal/core"
)

// Plan describes the requested processing shape. It is the single place where
// track presence and parameters become graph nodes, so the pipeline never
// builds nodes ad hoc.
type Plan struct {
	HaveAudioTrack bool
	SampleRate     int
	Channels       string
	TargetKbps     int
	LoudnessLUFS   float64
	PeakDBFS       float64
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
