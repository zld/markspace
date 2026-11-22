package pipeline

import (
	"bufio"
	"io"
)

// bufioWriter returns a buffered writer large enough to keep the million
// packet test I/O-bound rather than syscall-bound.
func bufioWriter(w io.Writer) *bufio.Writer { return bufio.NewWriterSize(w, 1<<20) }
