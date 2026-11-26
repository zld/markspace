package batch

import (
	"os"

	"mediarepair/internal/cache"
	"mediarepair/internal/media"
)

func closeStage(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func syncAndCloseStage(f *os.File) error {
	if f == nil {
		return nil
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return media.E(media.CodeWrite, "segment.sync", err)
	}
	if err := f.Close(); err != nil {
		return media.E(media.CodeWrite, "segment.close_stage", err)
	}
	return nil
}

func verifyCommittedSegment(root string, block int, wantHash string) error {
	got, err := cache.FileSHA256(segmentPath(root, block))
	if err != nil {
		return media.E(media.CodeVerification, "segment.committed_read", err).WithOffset(int64(block))
	}
	if got != wantHash {
		return media.E(media.CodeVerification, "segment.committed_hash", nil).WithOffset(int64(block))
	}
	return nil
}
