package evidence

import (
	"rtptrace/internal/recover"
	rtptrace "rtptrace/internal/rtp"
	"rtptrace/internal/track"
)

type canonicalRow struct {
	InputSample       string `json:"input_sample"`
	TrackID           uint32 `json:"track_id"`
	PresentationStart string `json:"presentation_start"`
	SegmentID         string `json:"segment_id"`
	Representation    string `json:"representation"`
	TimeErrorTicks    int64  `json:"time_error_ticks"`
	ByteError         int64  `json:"byte_error"`
	FailureTrack      uint32 `json:"failure_track"`
	FailureSample     uint32 `json:"failure_sample"`
	RecoveryState     string `json:"recovery_state"`
	SegmentIndex      int    `json:"segment_index"`
	BoundaryState     string `json:"boundary_state"`
	DecodeStartSample uint32 `json:"decode_start_sample"`
	ByteStart         int64  `json:"byte_start"`
	ByteEnd           int64  `json:"byte_end"`
}

type canonicalSummary struct {
	SchemaVersion      string                `json:"schema_version"`
	Seed               int64                 `json:"seed"`
	Fault              string                `json:"fault"`
	RecoverFlag        bool                  `json:"recover_flag"`
	ExitCode           int                   `json:"exit_code"`
	FailureTrack       uint32                `json:"failure_track"`
	FailureSample      uint32                `json:"failure_sample"`
	RecoveryState      string                `json:"recovery_state"`
	SegmentCount       int                   `json:"segment_count"`
	MappingCount       int                   `json:"mapping_count"`
	MaxTimeErrorTicks  int64                 `json:"max_time_error_ticks"`
	TotalByteError     int64                 `json:"byte_error_total"`
	GraphOrder         []uint32              `json:"track_processing_order"`
	RTP                *rtptrace.Report      `json:"rtp,omitempty"`
	RecoveryItems      []recover.Item        `json:"recovery_items"`
	EditReports        []recover.EditReport  `json:"edit_reports,omitempty"`
	AudioNormalization []*track.AudioPlan    `json:"audio_normalization,omitempty"`
	ColorDecisions     []track.ColorDecision `json:"color_decisions,omitempty"`
	Rows               []canonicalRow        `json:"rows"`
}

func (r *Report) computeSummary() string {
	rows := make([]canonicalRow, 0, len(r.Mappings))
	for _, m := range r.Mappings {
		rows = append(rows, canonicalRow{
			InputSample: m.InputSample, TrackID: m.TrackID,
			PresentationStart: m.PresentationStart, SegmentID: m.SegmentID,
			Representation: m.Representation, TimeErrorTicks: m.TimeErrorTicks,
			ByteError: m.ByteError, FailureTrack: m.FailureTrack,
			FailureSample: m.FailureSample, RecoveryState: m.RecoveryState,
			SegmentIndex: m.SegmentIndex, BoundaryState: m.BoundaryState,
			DecodeStartSample: m.DecodeStartSample, ByteStart: m.ByteStart, ByteEnd: m.ByteEnd,
		})
	}
	c := canonicalSummary{
		SchemaVersion: r.SchemaVersion, Seed: r.Seed, Fault: r.Fault,
		RecoverFlag: r.RecoverFlag, ExitCode: r.ExitCode,
		FailureTrack: r.FailureTrack, FailureSample: r.FailureSample,
		RecoveryState: r.RecoveryState, SegmentCount: r.SegmentCount,
		MappingCount: r.MappingCount, MaxTimeErrorTicks: r.MaxTimeErrorTicks,
		TotalByteError: r.TotalByteError, GraphOrder: r.GraphOrder, RTP: r.RTP,
		RecoveryItems: r.RecoveryItems, EditReports: r.EditReports,
		AudioNormalization: r.AudioNormalization, ColorDecisions: r.ColorDecisions,
		Rows: rows,
	}
	return SummaryHash(c)
}
