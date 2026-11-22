// Package stream executes the bounded pipeline: index-driven batch reads,
// repair, checkpointed output and explicit byte-accounted backpressure. The
// runner never holds more than its configured byte budget in flight, so a
// slow consumer stalls the producer instead of growing memory.
package stream

import (
	"context"

	"mediacore/internal/clock"
)

// Config is the execution policy. Both caps are hard limits enforced before
// every batch is admitted.
type Config struct {
	// MaxBatchSamples caps records per output batch.
	MaxBatchSamples int
	// MaxBatchBytes caps the sum of physical sample bytes per batch.
	MaxBatchBytes int64
	// MaxInFlightBytes bounds total producer-ahead buffering; this is the
	// number the demo's peak_buffer_bytes field reports against.
	MaxInFlightBytes int64
	// SlowEvery, when > 0, makes the demo consumer sleep every Nth batch.
	SlowEvery int
}

// Validate checks policy coherence and output-path safety.
func (c Config) Validate() error {
	if c.MaxBatchSamples <= 0 || c.MaxBatchBytes <= 0 || c.MaxInFlightBytes <= 0 {
		return clock.ErrInvalidArgument("stream: all batch and in-flight limits must be positive")
	}
	if c.MaxBatchBytes > c.MaxInFlightBytes {
		return clock.ErrInvalidArgument("stream: batch byte cap must not exceed in-flight budget")
	}
	if c.MaxInFlightBytes > 4<<30 {
		return clock.ErrInvalidArgument("stream: in-flight budget above 4 GiB safety ceiling")
	}
	return nil
}

// Batch is the unit handed to the consumer.
type Batch struct {
	Index      int // 0-based batch ordinal
	FirstSample uint32
	Records    []RecordView
}

// RecordView is the runner's read-only projection of an index record,
// augmented with the payload bytes read from the MP4 (bounded by the cap).
type RecordView struct {
	SampleIndex uint32
	Offset      uint64
	Size        uint32
	WireSize    uint32
	DTSNanos    int64
	CTSNanos    int64
	Sync        bool
	Payload     []byte
}

// Consumer processes one batch and returns its output size for accounting.
type Consumer interface {
	Consume(ctx context.Context, b Batch) (logicalBytes int64, err error)
}

// Counters are updated as the pipeline makes progress.
type Counters struct {
	BatchesEmitted   int
	SamplesEmitted   uint64
	BytesRead        int64 // physical mdat bytes actually read
	LogicalBytes     int64 // wire-size accounting used by metrics
	PeakInFlight     int64
	DroppedSamples   uint64
}
