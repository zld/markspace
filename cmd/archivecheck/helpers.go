package main

import (
	"io"

	"archivecheck/internal/pipeline"
)

func bytesReaderAt(b []byte) io.ReaderAt { return &readerAt{b: b} }

type readerAt struct{ b []byte }

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func allStagesShare(rep pipeline.Report, cause error) bool {
	for _, s := range rep.Stages {
		if s.Err == nil || s.Err.Error() != cause.Error() {
			return false
		}
	}
	return true
}
