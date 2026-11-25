package timeline

import (
	"github.com/zld/media-check/internal/clock"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
)

type SegmentView struct {
	mp4.Segment
	TimeRange       clock.Range `json:"time_range"`
	SourceTimeRange clock.Range `json:"source_time_range"`
}

type TrackView struct {
	TrackID       uint32      `json:"track_id"`
	Kind          string      `json:"kind"`
	Timescale     uint32      `json:"timescale"`
	DurationMS    int64       `json:"duration_ms"`
	SampleCount   int         `json:"sample_count"`
	TimelineRange clock.Range `json:"timeline_range"`
}

type DuplicateGroup struct {
	MaterialID string   `json:"material_id"`
	Count      int      `json:"count"`
	SegmentIDs []string `json:"segment_ids"`
}

type AlignmentReport struct {
	MaxErrorMS int64 `json:"max_error_ms"`
	Pairs      int   `json:"pairs"`
}

type Report struct {
	TimelineScale   uint32           `json:"timeline_scale"`
	Tracks          []TrackView      `json:"tracks"`
	Segments        []SegmentView    `json:"segments"`
	DuplicateGroups []DuplicateGroup `json:"duplicate_groups"`
	Alignment       AlignmentReport  `json:"alignment"`
	Issues          []*fault.Error   `json:"issues"`
}
