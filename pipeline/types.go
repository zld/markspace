package pipeline

import (
	"mediamux/analyze"
	"mediamux/cache"
	"mediamux/manifest"
)

// Input is one manifest plus the bytes available for each of its segments.
type Input struct {
	Manifest    *manifest.Document
	ManifestRaw string
	Segments    [][]byte
	// Nodes names the origin node for each segment, parallel to Segments.
	Nodes []string
	// RTP optionally carries transport packets per segment, parallel to
	// Segments. A nil entry means no RTP evidence for that segment.
	RTP [][][]byte
}

// Options bounds the runner's memory and timing behaviour.
type Options struct {
	MemoryLimit    int64
	SlowConsumerMS int64
	QueueDepth     int
	Window         int
	CheckpointPath string
	Epoch          int64
	// FakeSourceNode relabels the expected provenance on restore, which
	// demonstrates detection of a forged cache origin.
	FakeSourceNode string
	// ConsumerDelayMS sleeps in the consumer to exercise back pressure.
	ConsumerDelayMS int64
	// ProcessSegments caps how many segments this run handles, simulating an
	// interruption. Zero processes everything.
	ProcessSegments int
	// Dir roots on-disk cache and checkpoint state. Empty keeps state in
	// memory and disables checkpoint persistence.
	Dir string
}

// SegmentOutcome is one segment's committed result.
type SegmentOutcome struct {
	Index     int                     `json:"index"`
	ID        string                  `json:"id"`
	Node      string                  `json:"node"`
	Digest    string                  `json:"sha256"`
	Bytes     int64                   `json:"bytes"`
	FromCache bool                    `json:"from_cache"`
	Corrupt   bool                    `json:"corrupt"`
	Damaged   []analyze.DamagedRegion `json:"damaged,omitempty"`
	Quality   analyze.QualitySample   `json:"quality"`
	RTP       *analyze.RTPResult      `json:"rtp,omitempty"`
	Recovery  string                  `json:"recovery,omitempty"`
}

// Event is a stable, ordered diagnostic line the terminal adapter prints.
type Event struct {
	Kind    string `json:"kind"`
	Segment int    `json:"segment"`
	Detail  string `json:"detail"`
}

// Result is the complete evidence for one run.
type Result struct {
	Segments        []SegmentOutcome       `json:"segments"`
	Quality         analyze.QualitySummary `json:"quality"`
	Cache           cache.Stats            `json:"cache"`
	Checkpoint      *Checkpoint            `json:"checkpoint"`
	Resume          ResumeDecision         `json:"resume"`
	Events          []Event                `json:"events"`
	ResultSHA256    string                 `json:"result_sha256"`
	PeakBufferBytes int64                  `json:"peak_buffer_bytes"`
	SlowConsumer    bool                   `json:"slow_consumer"`
	CorruptSegments int                    `json:"corrupt_segments"`
}
