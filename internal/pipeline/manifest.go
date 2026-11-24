package pipeline

import (
	"medpipe/internal/clock"
	"medpipe/internal/media"
	"medpipe/internal/stream"
)

const (
	FormatManifest = "medpipe-manifest/v1"
	ParserMajor    = 1
	CheckpointName = "checkpoint.json"
	ManifestName   = "manifest.json"
	SourceName     = "source.mdp"
	ChecksumName   = "checksums.sha256"
	RejectionName  = "resume-rejected.json"
)

// Stats are deterministic input/analysis statistics. Scheduling-dependent
// queue counters are intentionally absent so identical inputs hash identically.
type Stats struct {
	SourceChunks     int64 `json:"source_chunks"`
	RTPPackets       int64 `json:"rtp_packets"`
	RTCPPackets      int64 `json:"rtcp_packets"`
	DeliveredItems   int64 `json:"delivered_items"`
	ValidFrames      int64 `json:"valid_frames"`
	ParseErrors      int   `json:"parse_errors"`
	CorruptPackets   int   `json:"corrupt_packets"`
	Duplicates       int64 `json:"duplicates"`
	Missing          int   `json:"missing"`
	OutOfOrder       int64 `json:"out_of_order"`
	ShortSilenceRuns int   `json:"short_silence_runs"`
}

// Manifest is the deterministic delivered-result document.
type Manifest struct {
	Format          string               `json:"format"`
	ParserMajor     int                  `json:"parser_major"`
	InputSHA256     string               `json:"input_sha256"`
	TimeUnit        string               `json:"time_unit"`
	SampleRate      uint32               `json:"sample_rate"`
	QueueLimitBytes int64                `json:"queue_limit_bytes"`
	Segments        []media.Segment      `json:"segments"`
	SilenceRanges   []media.SilenceRange `json:"silence_ranges"`
	Stats           Stats                `json:"stats"`
	Corruption      *clock.Location      `json:"corruption,omitempty"`
	ErrorCode       clock.ErrorCode      `json:"error_code,omitempty"`
}

// Result is the stable terminal output schema.
type Result struct {
	ExitCode          int                  `json:"exit_code"`
	Status            string               `json:"status"`
	Segments          []media.Segment      `json:"segments"`
	SilenceRanges     []media.SilenceRange `json:"silence_ranges"`
	Duplicates        int64                `json:"duplicates"`
	Missing           int                  `json:"missing"`
	QueuePeakBytes    int64                `json:"queue_peak_bytes"`
	QueueLimitBytes   int64                `json:"queue_limit_bytes"`
	BackpressureWaits int64                `json:"backpressure_waits,omitempty"`
	ManifestSHA256    string               `json:"manifest_sha256"`
	OutputSHA256      string               `json:"output_sha256,omitempty"`
	ManifestPath      string               `json:"manifest_path"`
	CheckpointPath    string               `json:"checkpoint_path"`
	CorruptPacketSeq  uint16               `json:"corrupt_packet_seq,omitempty"`
	CorruptByteOffset int64                `json:"corrupt_byte_offset,omitempty"`
	ErrorCode         clock.ErrorCode      `json:"error_code,omitempty"`
	ResumeMS          int64                `json:"resume_ms,omitempty"`
	ResumeFromSeq     uint16               `json:"resume_from_seq,omitempty"`
	ResumeFromExtSeq  uint64               `json:"resume_from_ext_seq,omitempty"`
	Interrupted       bool                 `json:"interrupted,omitempty"`
	PipeStats         *stream.PipeStats    `json:"pipe_stats,omitempty"`
}

// ResumeFailure carries a user-facing exit code in addition to the located
// validation error.
type ResumeFailure struct {
	Reason *clock.Error
}

func (e *ResumeFailure) Error() string { return e.Reason.Error() }
func (e *ResumeFailure) Unwrap() error { return e.Reason }
