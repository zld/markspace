package ebml

import (
	"io"

	"example.com/assetcheck/internal/timeline"
)

// Node is a parsed element plus its children in document order. Children of an
// unknown-length master are still attributed to that master because the reader
// only closes it at a legal parent boundary.
type Node struct {
	Element
	Children []*Node
	Parent   *Node
}

// Walk performs a depth-first traversal in document order. Returning false from
// fn skips the node's subtree but keeps its metadata.
type Walk func(n *Node) (descend bool)

// Tree is a fully parsed element tree plus the byte offset of each element.
type Tree struct {
	Root     *Node
	Count    int
	MaxDepth int
}

// Children returns direct children with the given ID.
func (n *Node) ChildrenOf(id uint32) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}

// Child returns the first direct child with the given ID.
func (n *Node) Child(id uint32) *Node {
	for _, c := range n.Children {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Depth returns the nesting level of the node (root is -1).
func (n *Node) Depth() int {
	d := -1
	for p := n.Parent; p != nil; p = p.Parent {
		d++
	}
	return d
}

// ParseTree parses the whole element tree without decoding payloads. Leaves are
// skipped in bounded steps, so memory tracks element count and not file size.
func ParseTree(src io.ReadSeeker, limits Limits) (*Tree, error) {
	r, err := NewReader(src, limits)
	if err != nil {
		return nil, err
	}
	root := &Node{Element: Element{ID: 0, Offset: 0, End: r.Size()}}
	tree := &Tree{Root: root}
	// stack[i] is the open master node at reader depth i.
	stack := []*Node{root}
	for {
		el, err := r.Next()
		if err == io.EOF {
			// Any remaining known-length frames were checked by Next.
			break
		}
		if err != nil {
			return nil, err
		}
		// The reader may have closed masters that ended at this element, so
		// align the node stack with the reader's current depth.
		if r.Depth() < len(stack)-1 {
			stack = stack[:r.Depth()+1]
		}
		parent := stack[len(stack)-1]
		n := &Node{Element: el, Parent: parent}
		parent.Children = append(parent.Children, n)
		tree.Count++
		if d := n.Depth(); d > tree.MaxDepth {
			tree.MaxDepth = d
		}
		if IsMaster(el.ID) {
			if err := r.Descend(el); err != nil {
				return nil, err
			}
			stack = append(stack, n)
			continue
		}
		if err := r.Skip(el); err != nil {
			return nil, err
		}
	}
	resolveUnknownEnds(root)
	return tree, nil
}

// resolveUnknownEnds anchors an unknown-length master's end at the end of its
// last child, which is exactly where the enclosing level's next sibling began.
// Unknown masters with no children end immediately after their header.
func resolveUnknownEnds(n *Node) {
	end := n.Payload()
	for _, c := range n.Children {
		resolveUnknownEnds(c)
		if c.End > end {
			end = c.End
		}
	}
	if n.Unknown {
		n.End = end
	}
}

// ReadLeaf returns up to max bytes of a leaf payload from the original input.
func ReadLeaf(src io.ReadSeeker, n *Node, max int64) ([]byte, error) {
	if n.Unknown {
		return nil, timeline.New(timeline.StageParse, timeline.CatUnknownLength, n.ID, n.Offset,
			"master %s has no flat payload", n.Name())
	}
	if int64(n.Size) > max {
		return nil, timeline.New(timeline.StageParse, timeline.CatLimitExceeded, n.ID, n.Offset,
			"payload %d exceeds read limit %d", n.Size, max)
	}
	b := make([]byte, n.Size)
	if _, err := src.Seek(n.Payload(), io.SeekStart); err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, n.ID, -1, "seek to payload")
	}
	if _, err := io.ReadFull(src, b); err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatTruncated, n.ID, n.Offset,
			"read payload")
	}
	return b, nil
}

// Fragment is a block element found under a Cluster, kept in document order so
// interleaved tracks can be compared by arrival order.
type Fragment struct {
	ClusterIndex int
	Order        int
	TrackNumber  uint64
	Timecode     int64 // raw track timecode units (block relative)
	ClusterTime  int64 // cluster Timecode in TimecodeScale units
	Offset       int64 // absolute offset of the block element
	HeaderSize   int64
	Size         uint64
	Kind         string // "SimpleBlock" or "BlockGroup"
}

// CollectFragments walks clusters in document order and decodes each block
// header. Only the block header is read; frame payloads stay on disk.
func CollectFragments(src io.ReadSeeker, tree *Tree) ([]Fragment, error) {
	var out []Fragment
	clusters := allChildrenOf(tree.Root, IDCluster)
	for ci, cluster := range clusters {
		clusterTime, err := ReadUint(src, cluster.Child(IDTimecode))
		if err != nil {
			return nil, err
		}
		order := 0
		for _, child := range cluster.Children {
			var (
				blockNode *Node
				kind      string
			)
			switch child.ID {
			case IDSimpleBlock:
				blockNode, kind = child, "SimpleBlock"
			case IDBlockGroup:
				b := child.Child(IDBlock)
				if b == nil {
					return nil, timeline.New(timeline.StageParse, timeline.CatMissingElement, child.ID, child.Offset,
						"BlockGroup at %d has no Block", child.Offset)
				}
				blockNode, kind = b, "BlockGroup"
			default:
				continue
			}
			track, rel, err := ReadBlockHeader(src, blockNode)
			if err != nil {
				return nil, err
			}
			out = append(out, Fragment{
				ClusterIndex: ci,
				Order:        order,
				TrackNumber:  track,
				Timecode:     rel,
				ClusterTime:  int64(clusterTime),
				Offset:       blockNode.Offset,
				HeaderSize:   blockNode.HeaderSize,
				Size:         blockNode.Size,
				Kind:         kind,
			})
			order++
		}
	}
	return out, nil
}

// allChildrenOf collects descendants with the given ID in document order.
func allChildrenOf(n *Node, id uint32) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.ID == id {
			out = append(out, c)
		}
		out = append(out, allChildrenOf(c, id)...)
	}
	return out
}

// ReadBlockHeader decodes the track number vint and signed 16-bit relative
// timecode that start every (Simple)Block. It reads only the block header.
func ReadBlockHeader(src io.ReadSeeker, n *Node) (track uint64, rel int64, err error) {
	if n.Size < 4 {
		return 0, 0, timeline.New(timeline.StageParse, timeline.CatIllegalLength, n.ID, n.Offset,
			"block payload %d bytes is too short for a header", n.Size)
	}
	head, err := ReadLeaf(src, n, 16)
	if err != nil {
		return 0, 0, err
	}
	// Track number: EBML vint with marker stripped.
	first := head[0]
	if first == 0 {
		return 0, 0, timeline.New(timeline.StageParse, timeline.CatIllegalLength, n.ID, n.Offset,
			"block track vint starts with zero")
	}
	l := 1
	for bit := 7; bit >= 0; bit-- {
		if first&(1<<uint(bit)) != 0 {
			l = 8 - bit
			break
		}
	}
	if l > 8 || len(head) < l+2 {
		return 0, 0, timeline.New(timeline.StageParse, timeline.CatIllegalLength, n.ID, n.Offset,
			"block header %d bytes cannot hold track vint of %d bytes plus timecode", len(head), l)
	}
	track = uint64(first & (1<<uint(8-l) - 1))
	for i := 1; i < l; i++ {
		track = track<<8 | uint64(head[i])
	}
	rel = int64(int16(uint16(head[l])<<8 | uint16(head[l+1])))
	return track, rel, nil
}

// UintWidths maps an element's valid payload widths. ReadUint accepts only these
// so an overlong integer cannot silently wrap.
var UintWidths = map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true}

// ReadUint reads a big-endian unsigned integer leaf. Missing nodes yield 0.
func ReadUint(src io.ReadSeeker, n *Node) (uint64, error) {
	if n == nil {
		return 0, nil
	}
	b, err := ReadLeaf(src, n, 8)
	if err != nil {
		return 0, err
	}
	if !UintWidths[len(b)] {
		return 0, timeline.New(timeline.StageParse, timeline.CatIllegalLength, n.ID, n.Offset,
			"integer element %s has %d-byte payload", n.Name(), len(b))
	}
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v, nil
}

// ReadString reads a leaf payload as a UTF-8 string with trailing NULs trimmed.
func ReadString(src io.ReadSeeker, n *Node) (string, error) {
	if n == nil {
		return "", nil
	}
	b, err := ReadLeaf(src, n, 4096)
	if err != nil {
		return "", err
	}
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return string(b), nil
}
