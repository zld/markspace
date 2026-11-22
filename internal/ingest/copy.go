package ingest

import (
	"io"

	"avcheck/internal/media"
)

// copyN copies exactly n bytes, reporting a truncated source as a typed
// error rather than a silent short copy.
func copyN(dst io.Writer, src io.Reader, n uint64) (int64, error) {
	written, err := io.CopyN(dst, src, int64(n))
	if err == io.EOF {
		return written, media.NewError(media.KindTruncated, 0, 0, written,
			"payload source shorter than %d bytes", n)
	}
	return written, err
}
