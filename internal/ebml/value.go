package ebml

import (
	"io"
	"math"

	"example.com/assetcheck/internal/timeline"
)

// ReadFloat reads a 4- or 8-byte IEEE-754 big-endian float leaf.
func ReadFloat(src io.ReadSeeker, n *Node) (float64, error) {
	if n == nil {
		return 0, nil
	}
	b, err := ReadLeaf(src, n, 8)
	if err != nil {
		return 0, err
	}
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binaryUint32(b))), nil
	case 8:
		return math.Float64frombits(binaryUint64(b)), nil
	case 0:
		return 0, nil
	default:
		return 0, timeline.New(timeline.StageParse, timeline.CatIllegalLength, n.ID, n.Offset,
			"float element %s has %d-byte payload", n.Name(), len(b))
	}
}

func binaryUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func binaryUint64(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

// ReadUintChild reads a child element by ID, returning 0 when absent.
func ReadUintChild(src io.ReadSeeker, parent *Node, id uint32) (uint64, error) {
	if parent == nil {
		return 0, nil
	}
	return ReadUint(src, parent.Child(id))
}

// RequireUint reads a mandatory integer child, reporting its own offset when
// missing so the error points into the file rather than at the parent.
func RequireUint(src io.ReadSeeker, parent *Node, id uint32) (uint64, error) {
	if parent == nil {
		return 0, timeline.New(timeline.StageParse, timeline.CatMissingElement, id, -1,
			"mandatory element %s has no parent", Name(id))
	}
	child := parent.Child(id)
	if child == nil {
		return 0, timeline.New(timeline.StageParse, timeline.CatMissingElement, id, parent.Offset,
			"mandatory element %s missing from %s at %d", Name(id), parent.Name(), parent.Offset)
	}
	return ReadUint(src, child)
}
