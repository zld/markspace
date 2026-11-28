package manifest

import (
	"hash/fnv"
	"strconv"
)

// hashBytes is the same FNV-1a hex digest used by scan and fixture so hashes
// are comparable across the whole pipeline.
func hashBytes(data []byte) string {
	h := fnv.New64a()
	h.Write(data)
	return strconv.FormatUint(h.Sum64(), 16)
}
