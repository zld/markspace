package fixture

import "encoding/binary"

// boxNode is an in-memory box tree used to serialize nested containers.
type boxNode struct {
	typ     string
	payload []byte // leaf payload (excluding header); nil when it has children
	child   []*boxNode
}

func leaf(typ string, payload []byte) *boxNode {
	return &boxNode{typ: typ, payload: payload}
}

func container(typ string, children ...*boxNode) *boxNode {
	return &boxNode{typ: typ, child: children}
}

// serialize renders a box tree into a flat byte slice.
func (n *boxNode) serialize() []byte {
	var payload []byte
	if n.child != nil {
		for _, c := range n.child {
			payload = append(payload, c.serialize()...)
		}
	} else {
		payload = n.payload
	}
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:8], []byte(n.typ))
	copy(out[8:], payload)
	return out
}
