package model

import (
	"fmt"
	"sort"
	"strings"

	"archive/internal/fault"
)

// ContainerKind names a supported demuxer.
type ContainerKind string

const (
	ContainerMP4 ContainerKind = "mp4"
	ContainerMXF ContainerKind = "mxf"
)

// TrackKind is the coarse media class of a track.
type TrackKind string

const (
	KindVideo TrackKind = "video"
	KindAudio TrackKind = "audio"
	KindData  TrackKind = "data"
)

// Crop describes a clean-aperture crop in pixels. A nil Crop means no crop
// was declared in the container.
type Crop struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}

// ColorField is a single colour property together with the source it was
// read from, so conflicts remain auditable instead of being flattened.
type ColorField struct {
	Field  string `json:"field,omitempty"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ColorDescription holds the resolved colour view plus every source value
// that participated in arbitration (MXF > MP4 > default).
type ColorDescription struct {
	Primaries    ColorField   `json:"primaries"`
	Transfer     ColorField   `json:"transfer"`
	Matrix       ColorField   `json:"matrix"`
	Range        ColorField   `json:"range"`
	Conflicts    []ColorField `json:"conflicts,omitempty"`
	ArbitratedBy string       `json:"arbitrated_by"`
}

// IndexInfo states whether the container index is usable. A track whose index
// is absent or partial must be recovered by scanning.
type IndexInfo struct {
	Present     bool   `json:"present"`
	Complete    bool   `json:"complete"`
	Entries     int    `json:"entries"`
	Source      string `json:"source,omitempty"`
	Reason      string `json:"reason,omitempty"`
	OffsetBytes uint64 `json:"offset_bytes"`
}

// Fragment is one data range of a track in the input file. PayloadOffset is
// the absolute byte offset of the essence bytes (the same bytes in every
// container, the unit that remux must copy unchanged). WrapperOffset is the
// start of the container wrapper around them (an MXF KLV key), or the same as
// PayloadOffset when the container has no wrapper (MP4 samples).
type Fragment struct {
	Seq           int    `json:"seq"`
	WrapperOffset uint64 `json:"wrapper_offset"`
	PayloadOffset uint64 `json:"payload_offset"`
	Size          uint64 `json:"size"` // essence size, excluding the wrapper
	WrapperSize   uint64 `json:"wrapper_size,omitempty"`
	StartTick     uint64 `json:"start_tick"`
	Duration      uint64 `json:"duration"`
	Sync          bool   `json:"sync"`
}

// EndTick is the exclusive end of the fragment on its track clock.
func (f Fragment) EndTick() uint64 { return f.StartTick + f.Duration }

// Track is the container-independent description of one elementary stream.
type Track struct {
	Key string `json:"key"`
	// Logical is the container-independent identity used to join several
	// container versions of the same archived reel (for example the MXF and
	// the MP4 copy of one video stream).
	Logical       string           `json:"logical"`
	Source        string           `json:"source"`
	Container     ContainerKind    `json:"container"`
	TrackID       uint32           `json:"track_id"`
	Kind          TrackKind        `json:"kind"`
	Codec         string           `json:"codec"`
	Required      bool             `json:"required"`
	Timebase      Timebase         `json:"timebase"`
	StartTick     uint64           `json:"start_tick"`
	DurationTick  uint64           `json:"duration_tick"`
	RotationDeg   int              `json:"rotation_deg"`
	EditMediaTime int64            `json:"edit_media_time,omitempty"`
	Crop          *Crop            `json:"crop,omitempty"`
	Color         ColorDescription `json:"color"`
	ColorClaims   []ColorField     `json:"color_claims,omitempty"`
	Index         IndexInfo        `json:"index"`
	Fragments     []Fragment       `json:"fragments,omitempty"`
	DependsOn     []string         `json:"depends_on,omitempty"`

	// MissingOptional records the names of optional metadata that the
	// container did not carry. Presence does not block mapping.
	MissingOptional []string `json:"missing_optional,omitempty"`
}

// DeclaredRange is the track extent on its own clock.
func (t *Track) DeclaredRange() Range {
	return Range{StartTick: t.StartTick, Duration: t.DurationTick}
}

// NodeKind is the type of a node in the dependency graph.
type NodeKind string

const (
	NodeContainer NodeKind = "container"
	NodeTrack     NodeKind = "track"
	NodeIndex     NodeKind = "index"
	NodeFragment  NodeKind = "fragment"
)

// Node is one vertex of the explicit dependency graph. Edges point at nodes
// that must be resolved before this node can be used.
type Node struct {
	ID       string   `json:"id"`
	Kind     NodeKind `json:"kind"`
	Ref      string   `json:"ref"`
	Required bool     `json:"required"`
	Deps     []string `json:"deps,omitempty"`
}

// Graph is the explicit container -> track -> index -> fragment graph.
type Graph struct {
	Nodes map[string]*Node `json:"nodes"`
	Order []string         `json:"order"`
}

// NewGraph creates an empty graph.
func NewGraph() *Graph { return &Graph{Nodes: make(map[string]*Node)} }

// Add inserts a node. Duplicate IDs are a programming error and reported.
func (g *Graph) Add(n *Node) error {
	if n == nil || n.ID == "" {
		return fmt.Errorf("graph: empty node")
	}
	if _, ok := g.Nodes[n.ID]; ok {
		return fmt.Errorf("graph: duplicate node %q", n.ID)
	}
	g.Nodes[n.ID] = n
	g.Order = append(g.Order, n.ID)
	return nil
}

// AddTrack appends the container, track, index and fragment nodes for a
// track, wiring the data-flow dependency edges. Track-level dependencies are
// added afterwards by WireTrackDeps.
func (g *Graph) AddTrack(t *Track) error {
	cid := ContainerNodeID(t.Container)
	if _, ok := g.Nodes[cid]; !ok {
		if err := g.Add(&Node{ID: cid, Kind: NodeContainer, Ref: string(t.Container), Required: true}); err != nil {
			return err
		}
	}
	tid := TrackNodeID(t.Key)
	if err := g.Add(&Node{ID: tid, Kind: NodeTrack, Ref: t.Key, Required: t.Required, Deps: []string{cid}}); err != nil {
		return err
	}
	iid := IndexNodeID(t.Key)
	indexRequired := t.Required
	if err := g.Add(&Node{ID: iid, Kind: NodeIndex, Ref: t.Key, Required: indexRequired, Deps: []string{tid}}); err != nil {
		return err
	}
	for i := range t.Fragments {
		fid := FragmentNodeID(t.Key, t.Fragments[i].Seq)
		if err := g.Add(&Node{ID: fid, Kind: NodeFragment, Ref: t.Key, Required: t.Required, Deps: []string{iid}}); err != nil {
			return err
		}
	}
	return nil
}

// WireTrackDeps adds the declared inter-track edges.
func (g *Graph) WireTrackDeps(tracks []*Track) error {
	for _, t := range tracks {
		n, ok := g.Nodes[TrackNodeID(t.Key)]
		if !ok {
			return fmt.Errorf("graph: track node %q missing", t.Key)
		}
		for _, dep := range t.DependsOn {
			did := TrackNodeID(dep)
			if _, ok := g.Nodes[did]; !ok {
				if t.Required {
					return &fault.Error{Code: fault.MissingRequiredTrack, Detail: fmt.Sprintf("graph: required track %q depends on absent track %q", t.Key, dep)}
				}
				continue
			}
			n.Deps = append(n.Deps, did)
		}
		sort.Strings(n.Deps)
	}
	return nil
}

// ContainerNodeID, TrackNodeID, IndexNodeID and FragmentNodeID are canonical.
func ContainerNodeID(k ContainerKind) string { return "container:" + string(k) }
func TrackNodeID(key string) string          { return "track:" + key }
func IndexNodeID(key string) string          { return "index:" + key }
func FragmentNodeID(key string, seq int) string {
	return fmt.Sprintf("fragment:%s:%d", key, seq)
}

// TopoOrder returns nodes in dependency order. Cycles and absent required
// dependencies are reported as faults, never as a partial success.
func (g *Graph) TopoOrder() ([]*Node, error) {
	state := make(map[string]uint8, len(g.Nodes)) // 1 visiting, 2 done
	order := make([]*Node, 0, len(g.Nodes))
	var visit func(id string, stack []string) error
	visit = func(id string, stack []string) error {
		n, ok := g.Nodes[id]
		if !ok {
			return &fault.Error{Code: fault.MissingRequiredTrack, Detail: fmt.Sprintf("graph: unresolved dependency %q", id)}
		}
		switch state[id] {
		case 2:
			return nil
		case 1:
			cycle := append(append([]string{}, stack...), id)
			return &fault.Error{Code: fault.DependencyCycle, Detail: "graph: cycle " + strings.Join(cycle, " -> ")}
		}
		state[id] = 1
		for _, dep := range n.Deps {
			if err := visit(dep, append(stack, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		order = append(order, n)
		return nil
	}
	// Stable traversal: sort IDs so output does not depend on map iteration.
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// TrackOrder returns only track nodes in dependency order. It is the order
// the mapper must follow before emitting any mapping.
func (g *Graph) TrackOrder() ([]string, error) {
	all, err := g.TopoOrder()
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, n := range all {
		if n.Kind == NodeTrack {
			keys = append(keys, n.Ref)
		}
	}
	return keys, nil
}

// Validate checks the graph invariants required before mapping: every
// required node resolves, and there are no cycles. Optional nodes that are
// absent are reported in missing, never as hard failures.
func (g *Graph) Validate(tracks []*Track) (missing []string, err error) {
	for _, t := range tracks {
		if t.Required {
			if _, ok := g.Nodes[TrackNodeID(t.Key)]; !ok {
				return nil, &fault.Error{Code: fault.MissingRequiredTrack, Detail: "graph: required track " + t.Key}
			}
		}
		for _, dep := range t.DependsOn {
			if _, ok := g.Nodes[TrackNodeID(dep)]; !ok {
				if t.Required {
					return nil, &fault.Error{Code: fault.MissingRequiredTrack, Detail: fmt.Sprintf("graph: %s requires %s", t.Key, dep)}
				}
				missing = append(missing, dep)
			}
		}
	}
	if _, err := g.TopoOrder(); err != nil {
		return nil, err
	}
	sort.Strings(missing)
	return missing, nil
}

// TrackGraph bundles the tracks with their dependency graph plus the order in
// which mapping may emit results.
type TrackGraph struct {
	Tracks    []*Track `json:"tracks"`
	Graph     *Graph   `json:"graph"`
	TrackKeys []string `json:"track_order"`
	Missing   []string `json:"missing_optional,omitempty"`
}

// BuildGraph constructs and validates the track graph.
func BuildGraph(tracks []*Track) (*TrackGraph, error) {
	g := NewGraph()
	sorted := append([]*Track(nil), tracks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	for _, t := range sorted {
		if err := g.AddTrack(t); err != nil {
			return nil, err
		}
	}
	if err := g.WireTrackDeps(sorted); err != nil {
		return nil, err
	}
	missing, err := g.Validate(sorted)
	if err != nil {
		return nil, err
	}
	order, err := g.TrackOrder()
	if err != nil {
		return nil, err
	}
	return &TrackGraph{Tracks: sorted, Graph: g, TrackKeys: order, Missing: missing}, nil
}

// ByKey looks up a track.
func (tg *TrackGraph) ByKey(key string) *Track {
	for _, t := range tg.Tracks {
		if t.Key == key {
			return t
		}
	}
	return nil
}
