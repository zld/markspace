// Package fanout distributes repaired blocks to several bitrate
// representations with independent recovery state. This file declares the
// ladder configuration and statistics types.
package fanout

import (
	"time"

	"mediad/internal/rtp"
)

// BufferBytes is the per-layer buffer budget required by the design.
const BufferBytes = 4 << 20

// LadderNames are the three representation names, lowest bitrate first.
var LadderNames = [3]string{"360p", "720p", "1080p"}

// Transport models the loss and reordering a layer's RTP path applies.
type Transport struct {
	// DropEvery drops every N-th block (0 disables), opening a sequence hole.
	DropEvery int
	// SwapEvery delivers block N after block N+1 (0 disables).
	SwapEvery int
	// Window is the reorder buffer in packets; capped at rtp.Window (64).
	Window int
}

func (t Transport) withDefaults() Transport {
	if t.Window <= 0 || t.Window > rtp.Window {
		t.Window = rtp.Window
	}
	return t
}

// LayerOptions configures one representation.
type LayerOptions struct {
	Name string
	// Buffer is the byte budget; defaults to BufferBytes.
	Buffer int
	// Slow makes the consumer take this long per block.
	Slow time.Duration
	// Transp is this layer's transport model.
	Transp Transport
}

func (o LayerOptions) withDefaults() LayerOptions {
	if o.Buffer <= 0 {
		o.Buffer = BufferBytes
	}
	o.Transp = o.Transp.withDefaults()
	return o
}

// LayerStats is a snapshot of one layer's recovery state.
type LayerStats struct {
	Name string
	// Offered counts blocks the publisher handed to Feed.
	Offered int
	// Dropped counts blocks the transport model discarded.
	Dropped int
	// Reordered counts blocks that arrived out of order and were put back.
	Reordered int
	// Lost counts holes the reorder window declared unrecoverable.
	Lost int
	// Delivered counts blocks handed to the consumer.
	Delivered int
	// BufferedBytes is the current byte-budget usage; PeakBytes its high
	// water mark, which must never exceed the budget.
	BufferedBytes int
	PeakBytes     int
	// BackpressureNs is time Feed spent waiting for buffer space.
	BackpressureNs int64
	Canceled       bool
	Closed         bool
}
