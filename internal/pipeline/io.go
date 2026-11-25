package pipeline

import "bytes"

// newBytesReader keeps the io.Reader construction in one place so pipeline
// tests can swap it without touching the stage wiring.
func newBytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }
