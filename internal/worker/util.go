package worker

import (
	"context"
	"os"
	"strconv"
	"strings"

	"regress/internal/scan"
)

// scanFile opens and parses an elementary stream produced by an encode.
func scanFile(codec, path string) (*scan.ScanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return scan.ScanElementary(codec, f)
}

// statSize returns the on-disk size of a produced file.
func statSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// writeLog persists an SSIM log for postmortem inspection; failures are
// non-fatal because the in-memory buffer already carries the data.
func writeLog(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// idFromPath recovers a candidate id from a trial output file name.
func idFromPath(path string) int {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	base = strings.TrimPrefix(base, "trial-")
	if i := strings.IndexAny(base, "."); i >= 0 {
		base = base[:i]
	}
	v, err := strconv.Atoi(base)
	if err != nil {
		return -1
	}
	return v
}

// isTimeout reports whether a subprocess error was caused by a deadline.
func isTimeout(ctx context.Context, err error) bool {
	return ctx.Err() == context.DeadlineExceeded && err != nil
}
