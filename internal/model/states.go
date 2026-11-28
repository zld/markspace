package model

// Pipeline states shared by the stream state machine and the manifest. They
// live in model so lower-level packages (manifest) need not import stream.
const (
	StateIndexed   = "indexed"
	StatePlanned   = "planned"
	StateStreaming = "streaming"
	StateCommitted = "committed"
)
