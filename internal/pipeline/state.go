package pipeline

import (
	"encoding/json"
	"path/filepath"
	"time"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
	"medpipe/internal/media"
	"medpipe/internal/sample"
)

// Options controls one pipeline execution.
type Options struct {
	OutDir               string
	QueueBytes           int64
	ConsumerDelay        time.Duration
	CheckpointEvery      int
	SegmentDuration      time.Duration
	SegmentMaxDuration   time.Duration
	SilenceThreshold     int
	SilenceMinDuration   time.Duration
	InterruptAfterChunks int64
}

// State is the complete resumable analysis state stored in checkpoint.json.
type State struct {
	Timeline         clock.TimelineState  `json:"timeline"`
	Reorder          ingest.ReorderState  `json:"reorder"`
	Segmenter        media.SegmenterState `json:"segmenter"`
	Silence          media.SilenceState   `json:"silence"`
	Deduper          media.DeduperState   `json:"deduper"`
	Segments         []media.Segment      `json:"segments"`
	SilenceRanges    []media.SilenceRange `json:"silence_ranges"`
	Stats            Stats                `json:"stats"`
	FirstError       *clock.Error         `json:"first_error,omitempty"`
	ErrorCode        clock.ErrorCode      `json:"error_code,omitempty"`
	ProcessedChunks  int64                `json:"processed_chunks"`
	NextSourceOffset int64                `json:"next_source_offset"`
	LastSequence     uint16               `json:"last_sequence"`
	LastExtSeq       uint64               `json:"last_ext_sequence"`
}

func (s *State) ensureSlices() {
	if s.Segments == nil {
		s.Segments = []media.Segment{}
	}
	if s.SilenceRanges == nil {
		s.SilenceRanges = []media.SilenceRange{}
	}
}

func checkpointPath(out string) string { return filepath.Join(out, CheckpointName) }
func manifestPath(out string) string   { return filepath.Join(out, ManifestName) }
func sourcePath(out string) string     { return filepath.Join(out, SourceName) }
func checksumPath(out string) string   { return filepath.Join(out, ChecksumName) }
func rejectionPath(out string) string  { return filepath.Join(out, RejectionName) }

func marshalGeneratorConfig(cfg sample.Config) json.RawMessage {
	b, _ := json.Marshal(cfg)
	return b
}

func unmarshalGeneratorConfig(raw json.RawMessage) sample.Config {
	cfg := sample.DefaultConfig()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	return cfg.WithDefaults()
}
