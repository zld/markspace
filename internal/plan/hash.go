package plan

import (
	"hash"
	"strconv"
)

// writeInt appends a canonical decimal representation with a separator so that
// adjacent fields can never be ambiguous.
func writeInt(h hash.Hash, v int64) {
	h.Write([]byte(strconv.FormatInt(v, 10)))
	h.Write([]byte{';'})
}

// writeFloat appends a fixed-precision canonical representation.
func writeFloat(h hash.Hash, v float64) {
	h.Write([]byte(strconv.FormatFloat(v, 'f', 6, 64)))
	h.Write([]byte{';'})
}
