package manifest

import (
	"os"

	"liveregress/internal/fault"
)

// MaxSegmentBytes bounds a single declared byte range. Callers may lower it;
// nothing in the pipeline reads an unbounded segment into memory.
const MaxSegmentBytes int64 = 64 << 20

// validateRange checks that a declared byte range is within the resource and
// within MaxSegmentBytes. absPath is used for the filesystem check while the
// fault names relPath, which is what a user sees in the playlist.
func validateRange(absPath, relPath string, br *ByteRange) error {
	if br.Length <= 0 {
		return fault.New(fault.KindCorrupt, relPath, br.Offset, "byte range has non-positive length")
	}
	if br.Offset < 0 {
		return fault.New(fault.KindFormat, relPath, br.Offset, "byte range has negative offset")
	}
	if br.Length > MaxSegmentBytes {
		return fault.New(fault.KindLimit, relPath, br.Offset,
			"byte range exceeds the per-segment limit").Recover(true)
	}
	info, statErr := os.Stat(absPath)
	if statErr != nil {
		return fault.Wrap(fault.KindPath, relPath, br.Offset, "referenced resource is unreadable", statErr)
	}
	if br.Offset+br.Length > info.Size() {
		return fault.New(fault.KindTruncate, relPath, br.Offset,
			"byte range extends past end of resource").Recover(false)
	}
	return nil
}
