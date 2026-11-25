// Package report renders the quantitative replay summary. The JSON schema is
// deterministic: collections keep processing order and floating-point
// millisecond values are rounded to a fixed precision so a repaired rerun
// produces byte-identical output to the healthy reference.
package report

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"rtpreplay/audio"
	"rtpreplay/fingerprint"
	"rtpreplay/media"
	"rtpreplay/pipeline"
	"rtpreplay/segment"
)

// msPrecision fixes millisecond output to microsecond resolution.
const msPrecision = 1000

// SegmentReport is the JSON view of one segment.
type SegmentReport struct {
	Index       int      `json:"index"`
	BitrateKbps int      `json:"bitrate_kbps"`
	Kind        string   `json:"kind"`
	StartMS     float64  `json:"start_ms"`
	EndMS       float64  `json:"end_ms"`
	StartRecord int      `json:"start_record"`
	EndRecord   int      `json:"end_record"`
	StartExtSeq uint64   `json:"start_ext_seq"`
	EndExtSeq   uint64   `json:"end_ext_seq"`
	Damaged     bool     `json:"damaged"`
	Reasons     []string `json:"reasons,omitempty"`
}

// RangeReport exposes an isolated anomaly interval with its record span.
type RangeReport struct {
	Reason      string `json:"reason"`
	StartRecord int    `json:"start_record"`
	EndRecord   int    `json:"end_record"`
}

// RepairEvidence documents one interpolated recovery for auditability.
type RepairEvidence struct {
	BitrateKbps    int     `json:"bitrate_kbps"`
	ExtSeq         uint64  `json:"ext_seq"`
	TimeMS         float64 `json:"time_ms"`
	BetweenRecords [2]int  `json:"between_records"`
}

// Summary carries every acceptance-required field plus the evidence trail.
type Summary struct {
	SyncErrorMS      float64                 `json:"sync_error_ms"`
	SyncFailed       bool                    `json:"sync_failed"`
	LossRecoveryRate float64                 `json:"loss_recovery_rate"`
	LatencyP95MS     float64                 `json:"latency_p95_ms"`
	DriftPPM         map[string]float64      `json:"drift_ppm"`
	Segments         []SegmentReport         `json:"segments"`
	SilenceRegions   []audio.SilenceRegion   `json:"silence_regions"`
	Duplicates       []fingerprint.Duplicate `json:"duplicates"`
	IsolatedRanges   []RangeReport           `json:"isolated_ranges"`
	Repairs          []RepairEvidence        `json:"recovery_evidence"`
	ErrorCode        string                  `json:"error_code,omitempty"`
	ErrorOffset      *int64                  `json:"error_offset"`
	severity         int                     `json:"-"`
}

// Build assembles the summary from pipeline outputs. Inputs keep their
// processing order; no cross-representation state is merged here.
func Build(
	reps []*pipeline.Representation,
	sync *pipeline.SyncResult,
	segs []segment.Segment,
	silence []audio.SilenceRegion,
	dups []fingerprint.Duplicate,
) *Summary {
	s := &Summary{
		DriftPPM:       map[string]float64{},
		Segments:       []SegmentReport{},
		SilenceRegions: nonNilSilence(silence),
		Duplicates:     nonNilDups(dups),
		IsolatedRanges: []RangeReport{},
		Repairs:        []RepairEvidence{},
	}
	if sync != nil {
		s.SyncErrorMS = roundMS(sync.SyncErrorMS)
		s.SyncFailed = sync.Failed
	}
	s.LossRecoveryRate = roundRate(pipeline.LossRecoveryRate(reps))
	s.LatencyP95MS = roundMS(pipeline.LatencyP95MS(reps))

	for _, r := range reps {
		key := bitrateKey(r)
		s.DriftPPM[key] = roundPPM(r.Stream.DriftPPM)
		for _, rg := range r.Stream.IsolatedRanges() {
			s.IsolatedRanges = append(s.IsolatedRanges, RangeReport{
				Reason: string(rg.Reason), StartRecord: rg.StartRecord, EndRecord: rg.EndRecord,
			})
		}
		for _, rp := range r.Recovered {
			s.Repairs = append(s.Repairs, RepairEvidence{
				BitrateKbps: r.BitrateKbps, ExtSeq: rp.ExtSeq,
				TimeMS: roundMS(rp.TimeMS), BetweenRecords: rp.BetweenRec,
			})
		}
	}
	for _, sg := range segs {
		s.Segments = append(s.Segments, SegmentReport{
			Index: sg.Index, BitrateKbps: sg.BitrateKbps, Kind: sg.Kind,
			StartMS: roundMS(sg.StartMS), EndMS: roundMS(sg.EndMS),
			StartRecord: sg.StartRecord, EndRecord: sg.EndRecord,
			StartExtSeq: sg.StartExtSeq, EndExtSeq: sg.EndExtSeq,
			Damaged: sg.Damaged, Reasons: sg.Reasons,
		})
	}
	return s
}

// AttachError records a structured error offset/code for damaged runs.
// Errors are ranked by severity: an unrecoverable failure is never
// overwritten by a lower-severity metadata/parse observation. The first
// error at a given severity is retained so original offsets survive.
func (s *Summary) AttachError(err error) {
	le, ok := media.AsLocatable(err)
	if !ok {
		return
	}
	if s.severity < severity(le.Kind) ||
		(s.severity == severity(le.Kind) && s.ErrorCode == "") {
		s.ErrorCode = le.Code
		if le.Offset >= 0 {
			off := le.Offset
			s.ErrorOffset = &off
		}
		s.severity = severity(le.Kind)
	}
}

func severity(k media.Kind) int {
	switch k {
	case media.KindUnrecoverable:
		return 3
	case media.KindConflict:
		return 2
	case media.KindUnsupported:
		return 1
	default:
		return 0
	}
}

// WriteJSON emits the summary with stable formatting. It refuses to write
// inside the input directory, keeping generated evidence separate from data.
func (s *Summary) WriteJSON(outputPath, inputDir string) error {
	if err := ensureOutsideInput(outputPath, inputDir); err != nil {
		return err
	}
	if dir := filepath.Dir(outputPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return media.NewError(media.KindBadInput, "OUTPUT_MKDIR", "report", -1, "%v", err)
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return media.NewError(media.KindBadInput, "JSON_MARSHAL", "report", -1, "%v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return media.NewError(media.KindBadInput, "OUTPUT_WRITE", "report", -1, "%v", err)
	}
	return nil
}

// ensureOutsideInput rejects output paths contained in the input directory.
func ensureOutsideInput(outputPath, inputDir string) error {
	if inputDir == "" {
		return nil
	}
	inAbs, err := filepath.Abs(filepath.Clean(inputDir))
	if err != nil {
		return media.NewError(media.KindBadInput, "BAD_INPUT_PATH", "report", -1, "%v", err)
	}
	outAbs, err := filepath.Abs(filepath.Clean(outputPath))
	if err != nil {
		return media.NewError(media.KindBadInput, "BAD_OUTPUT_PATH", "report", -1, "%v", err)
	}
	rel, err := filepath.Rel(inAbs, outAbs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel) {
		return media.NewError(media.KindBadInput, "OUTPUT_IN_INPUT_DIR", "report", -1,
			"output %s must not be written inside input directory %s", outAbs, inAbs)
	}
	return nil
}

func bitrateKey(r *pipeline.Representation) string {
	return r.Kind + "@" + strconv.Itoa(r.BitrateKbps) + "kbps"
}

func roundMS(v float64) float64   { return math.Round(v*msPrecision) / msPrecision }
func roundPPM(v float64) float64  { return math.Round(v*1e6) / 1e6 }
func roundRate(v float64) float64 { return math.Round(v*1e4) / 1e4 }

func nonNilSilence(in []audio.SilenceRegion) []audio.SilenceRegion {
	if in == nil {
		return []audio.SilenceRegion{}
	}
	return in
}

func nonNilDups(in []fingerprint.Duplicate) []fingerprint.Duplicate {
	if in == nil {
		return []fingerprint.Duplicate{}
	}
	return in
}
