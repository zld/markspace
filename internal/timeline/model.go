package timeline

import "fmt"

// Stage identifies the processing stage a fault or graph node belongs to.
type Stage string

const (
	StageIngest   Stage = "ingest"
	StageScan     Stage = "scan"
	StageDamage   Stage = "damage"
	StageKeyframe Stage = "keyframe"
	StageManifest Stage = "manifest"
)

// Fault is the shared structured error model. Every module reports problems
// as *Fault so callers can locate damage by track, stage and stream offset.
type Fault struct {
	Track  string // logical track id, e.g. "audio/rtp" or "video/mp4"
	Stage  Stage
	Offset int64 // byte or sample offset within the track stream; -1 if n/a
	Seq    int64 // extended packet sequence number; -1 if n/a
	Msg    string
}

func (f *Fault) Error() string {
	s := fmt.Sprintf("track=%s stage=%s", f.Track, f.Stage)
	if f.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", f.Offset)
	}
	if f.Seq >= 0 {
		s += fmt.Sprintf(" seq=%d", f.Seq)
	}
	return s + ": " + f.Msg
}

// Range locates a damaged region both by stream position (extended packet
// sequence numbers or sample offsets) and by media time on the track clock.
type Range struct {
	Track       string `json:"track"`
	Reason      string `json:"reason"`
	StartSeq    int64  `json:"startSeq,omitempty"`
	EndSeq      int64  `json:"endSeq,omitempty"`
	StartOffset int64  `json:"startOffset"`
	EndOffset   int64  `json:"endOffset"`
	Start       Ticks  `json:"startTicks"`
	End         Ticks  `json:"endTicks"`
}

// Node is a vertex of the explicit track/processing graph.
type Node struct {
	ID    string
	Stage Stage
	Deps  []string
}

// Graph declares processing dependencies explicitly. Ordering is computed by
// the pipeline package; this type is only the shared declaration.
type Graph struct {
	Nodes []Node
}

// Find returns the node with the given id, or nil.
func (g *Graph) Find(id string) *Node {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}
