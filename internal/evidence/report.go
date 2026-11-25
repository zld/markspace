// Package evidence assembles deterministic mappings, failures and summaries.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"rtptrace/internal/recover"
	rtptrace "rtptrace/internal/rtp"
	"rtptrace/internal/segment"
	"rtptrace/internal/track"
)

// Row is the flat evidence contract. Every row contains all required keys.
type Row struct {
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
	SummarySHA256     string `json:"summary_sha256"`

	SegmentIndex       int    `json:"segment_index"`
	BoundaryState      string `json:"boundary_state"`
	DecodeStartSample  uint32 `json:"decode_start_sample"`
	SegmentTargetStart string `json:"segment_target_start"`
	ByteStart          int64  `json:"byte_start"`
	ByteEnd            int64  `json:"byte_end"`
}

// Report is the stable JSON acceptance artifact.
type Report struct {
	SchemaVersion string `json:"schema_version"`
	Seed          int64  `json:"seed"`
	Fault         string `json:"fault"`
	RecoverFlag   bool   `json:"recover_flag"`
	GeneratedAt   string `json:"generated_at"` // intentionally empty: reruns must be identical

	// Flat required fields at top level, initialized from the first row.
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
	SummarySHA256     string `json:"summary_sha256"`

	ExitCode          int   `json:"exit_code"`
	PublishAllowed    bool  `json:"publish_allowed"`
	SegmentCount      int   `json:"segment_count"`
	MappingCount      int   `json:"mapping_count"`
	MaxTimeErrorTicks int64 `json:"max_time_error_ticks"`
	TotalByteError    int64 `json:"byte_error_total"`
	LossCount         int   `json:"loss_count"`
	DuplicateCount    int   `json:"duplicate_count"`
	OutOfOrderCount   int   `json:"out_of_order_count"`
	LateCount         int   `json:"late_count"`

	GraphOrder         []uint32                         `json:"track_processing_order"`
	TrackStates        []*track.Node                    `json:"track_states,omitempty"`
	AudioNormalization []*track.AudioPlan               `json:"audio_normalization,omitempty"`
	ColorDecisions     []track.ColorDecision            `json:"color_decisions,omitempty"`
	Boundaries         []segment.TrackBoundaries        `json:"boundaries,omitempty"`
	Representations    []segment.RepresentationManifest `json:"representation_manifests,omitempty"`
	RTP                *rtptrace.Report                 `json:"rtp,omitempty"`
	RecoveryItems      []recover.Item                   `json:"recovery_items"`
	EditReports        []recover.EditReport             `json:"edit_reports,omitempty"`
	Mappings           []Row                            `json:"mappings"`
}

// Input is the completed deterministic pipeline state.
type Input struct {
	Seed           int64
	Fault          string
	RecoverFlag    bool
	ExitCode       int
	PublishAllowed bool
	GraphOrder     []uint32
	TrackStates    map[uint32]*track.Node
	AudioPlans     []*track.AudioPlan
	ColorDecisions []track.ColorDecision
	Alignment      *segment.AlignedReport
	Trace          *rtptrace.Report
	RecoveryItems  []recover.Item
	EditReports    []recover.EditReport
}

// Build converts subsystem outputs into flat evidence rows and a stable summary.
func Build(in Input) *Report {
	r := &Report{
		SchemaVersion: "rtptrace.report/v1", Seed: in.Seed, Fault: in.Fault,
		RecoverFlag: in.RecoverFlag, GeneratedAt: "", ExitCode: in.ExitCode,
		PublishAllowed: in.PublishAllowed, RTP: in.Trace,
		RecoveryItems: append([]recover.Item{}, in.RecoveryItems...),
		EditReports:   append([]recover.EditReport{}, in.EditReports...),
		Mappings:      []Row{}, GraphOrder: append([]uint32{}, in.GraphOrder...),
		AudioNormalization: in.AudioPlans, ColorDecisions: in.ColorDecisions,
	}
	if in.Alignment != nil {
		r.SegmentCount = in.Alignment.SegmentCount
		r.MaxTimeErrorTicks = in.Alignment.MaxTimeErrorTicks
		r.TotalByteError = in.Alignment.TotalByteError
		r.Boundaries = in.Alignment.Boundaries
		r.Representations = in.Alignment.Representations
	}
	for _, id := range in.GraphOrder {
		if n := in.TrackStates[id]; n != nil {
			r.TrackStates = append(r.TrackStates, n)
		}
	}

	if in.Trace != nil {
		r.LossCount = in.Trace.Counts.Lost
		r.DuplicateCount = in.Trace.Counts.Duplicate
		r.OutOfOrderCount = in.Trace.Counts.OutOfOrder
		r.LateCount = in.Trace.Counts.Late
	}
	primaryTrack, primarySample, primaryState := recover.Primary(in.RecoveryItems)
	r.FailureTrack = primaryTrack
	r.FailureSample = primarySample
	r.RecoveryState = primaryState
	r.ByteError = r.TotalByteError

	if in.Alignment != nil {
		for _, m := range in.Alignment.Mappings {
			fTrack, fSample, state := rowFailure(m, in.RecoveryItems)
			row := Row{
				InputSample: m.InputSample, TrackID: m.TrackID,
				PresentationStart: m.PresentationStart, SegmentID: m.SegmentID,
				Representation: m.Representation, TimeErrorTicks: m.TimeErrorTicks,
				ByteError: m.ByteError, FailureTrack: fTrack, FailureSample: fSample,
				RecoveryState: state, SegmentIndex: m.SegmentIndex,
				BoundaryState: m.BoundaryState, DecodeStartSample: m.DecodeStartSample,
				SegmentTargetStart: m.SegmentTargetStart, ByteStart: m.ByteStart,
				ByteEnd: m.ByteEnd,
			}
			r.Mappings = append(r.Mappings, row)
		}
	}
	r.MappingCount = len(r.Mappings)
	if len(r.Mappings) > 0 {
		first := r.Mappings[0]
		r.InputSample = first.InputSample
		r.TrackID = first.TrackID
		r.PresentationStart = first.PresentationStart
		r.SegmentID = first.SegmentID
		r.Representation = first.Representation
		r.TimeErrorTicks = first.TimeErrorTicks
		if r.FailureTrack == 0 {
			r.FailureTrack = first.FailureTrack
			r.FailureSample = first.FailureSample
			r.RecoveryState = first.RecoveryState
		}
	} else {
		r.InputSample = "none"
		r.SegmentID = "none"
		r.Representation = "none"
		r.TrackID = r.FailureTrack
		r.PresentationStart = "none"
	}
	if r.RecoveryState == "" {
		r.RecoveryState = recover.StateOK
	}
	r.SummarySHA256 = r.computeSummary()
	for i := range r.Mappings {
		r.Mappings[i].SummarySHA256 = r.SummarySHA256
	}
	return r
}

func rowFailure(m segment.Mapping, items []recover.Item) (uint32, uint32, string) {
	for _, it := range items {
		if it.TrackID == m.TrackID && it.RecoveryState != recover.StateOK {
			// Boundary failures are more specific to the row's segment.
			if m.BoundaryState != segment.BoundaryAligned && m.BoundaryState != "" {
				return it.TrackID, boundarySample(m), segment.BoundaryMissingKey
			}
			return it.TrackID, it.FailureSample, it.RecoveryState
		}
	}
	if m.BoundaryState != segment.BoundaryAligned && m.BoundaryState != "" {
		return m.TrackID, boundarySample(m), recover.StateFailed
	}
	return 0, 0, recover.StateOK
}

func boundarySample(m segment.Mapping) uint32 {
	return m.FailureSample
}

// JSON renders indented JSON deterministically.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// SummaryHash returns the 64-character hex SHA-256 canonical summary.
func SummaryHash(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
