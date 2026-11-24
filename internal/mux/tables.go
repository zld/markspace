package mux

import (
	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/demux"
	"medpipe/internal/fault"
)

// writeMINF is replaced by the complete sample-table writer in the next
// incremental batch; the skeleton keeps the package compilable.
func writeMINF(w *gomp4.Writer, t *demux.Track, offsets []uint32) *fault.Error {
	_ = w
	_ = t
	_ = offsets
	return nil
}
