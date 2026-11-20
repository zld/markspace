package index

import (
	"fmt"
	"mp4seek/internal/bmff"
)

// Skeleton: filled in by a follow-up commit.
func buildTrun(r *bmff.Reader, trun bmff.Box, fl, dd, ds, df uint32, t TrexDefaults, i *FragIndex, dts, off int64) (int64, int64, error) {
	return 0, 0, fmt.Errorf("buildTrun not implemented")
}
