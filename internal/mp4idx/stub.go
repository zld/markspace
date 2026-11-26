package mp4idx

import (
	"mediamerge/internal/fault"
)

// build is replaced by the full sample-table reconstruction in a later commit.
func (rd *Reader) build(raw *rawTables) (*Index, error) {
	return nil, fault.New(fault.StageIndex, fault.KindIndex, rd.source,
		"sample-table reconstruction not yet available")
}
