package pipeline

import (
	"io"

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
	// Sources, when set, supplies each segment as a stream instead of a byte
	// slice. The runner then reads it in bounded chunks under the context, so
	// a canceled run cannot leave a blocked read behind.
	Sources []io.ReadCloser
	// SourceSizes declares each stream's expected byte length, parallel to
	// Sources, so the memory budget can be checked before reading.
	SourceSizes []int64
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
	// ConsumerDelayMS sleeps in the consumer after every delivery, applying
	// steady back pressure to the producer.
	ConsumerDelayMS int64
	// StallBeforeFirst makes the consumer sleep once, before taking any
	// delivery. Combined with a full queue this deterministically trips the
	// slow-consumer budget regardless of how fast analysis runs.
	StallBeforeFirst bool
	StallMS          int64
	// ProcessSegments caps how many segments this run handles, simulating an
	// interruption. Zero processes everything.
	ProcessSegments int
	// Dir roots on-disk cache and checkpoint state. Empty keeps state in
	// memory and disables checkpoint persistence.
	Dir string
	// CacheLimit bounds stored segment bytes. Zero derives a limit from
	// MemoryLimit for in-memory stores and a larger default on disk.
	CacheLimit int64
}

// SegmentOutcome is one segment's committed result.
type SegmentOutcome struct {
	Index        int                     `json:"index"`
	ID           string                  `json:"id"`
	Node         string                  `json:"node"`
	Digest       string                  `json:"sha256"`
	Bytes        int64                   `json:"bytes"`
	FromCache    bool                    `json:"from_cache"`
	Corrupt      bool                    `json:"corrupt"`
	Damaged      []analyze.DamagedRegion `json:"damaged,omitempty"`
	Quality      analyze.QualitySample   `json:"quality"`
	KeyframeGaps []int64                 `json:"keyframe_gaps_ms,omitempty"`
	RTP          *analyze.RTPResult      `json:"rtp,omitempty"`
	Recovery     string                  `json:"recovery,omitempty"`
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
	Alignment       *AlignmentReport       `json:"alignment,omitempty"`
	ResultSHA256    string                 `json:"result_sha256"`
	PeakBufferBytes int64                  `json:"peak_buffer_bytes"`
	SlowConsumer    bool                   `json:"slow_consumer"`
	CorruptSegments int                    `json:"corrupt_segments"`
}
