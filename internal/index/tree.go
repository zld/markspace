package index

import "github.com/abema/go-mp4"

// findType returns the first direct child of the given box type.
func findType(nodes []*boxNode, t mp4.BoxType) *boxNode {
	for _, n := range nodes {
		if n.info.Type == t {
			return n
		}
	}
	return nil
}

// findAll returns every direct child matching the box type.
func findAll(nodes []*boxNode, t mp4.BoxType) []*boxNode {
	var out []*boxNode
	for _, n := range nodes {
		if n.info.Type == t {
			out = append(out, n)
		}
	}
	return out
}

// payloadAs type-asserts a node payload, returning nil when absent.
func payloadAs[T mp4.IBox](n *boxNode) T {
	if n == nil || n.payload == nil {
		var zero T
		return zero
	}
	if p, ok := n.payload.(T); ok {
		return p
	}
	var zero T
	return zero
}

func offsetOf(n *boxNode) int64 {
	if n == nil {
		return -1
	}
	return int64(n.info.Offset)
}
