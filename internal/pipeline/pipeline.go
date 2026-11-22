// Package pipeline wires ingest, recovery, fan-out, fan-in and indexing into
// one deterministic run.
//
// The pipeline is the only place that knows the stage order:
//
//	parse -> clock repair -> per-domain recovery -> fan-out -> fan-in -> index
//
// It owns the run-wide limits (2 GiB input, 1 GiB event index memory) and
// produces a RunReport whose fields are exactly the demo's printed metrics.
package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"example.com/mediatimeline/internal/audio"
	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/fanin"
	"example.com/mediatimeline/internal/fanout"
	"example.com/mediatimeline/internal/index"
	"example.com/mediatimeline/internal/ingest"
	"example.com/mediatimeline/internal/model"
	"example.com/mediatimeline/internal/video"
)

// Input is everything a run consumes. All fields are in memory; the caller
// constructs them from fixtures or files.
type Input struct {
	MP4      []byte
	VideoRTP [][]byte
	AudioRTP [][]byte
	WebVTT   []byte
	Chapters []byte
	ID3      [][]byte
	ID3Ticks []int64
	Emsg     [][]byte
	SEI      []byte
	// AudioLayouts and ColorDecls are the competing declarations from
	// container, bitstream and manifest.
	AudioLayouts []model.AudioDescriptor
	ColorDecls   []model.ColorDeclaration
	// AudioRateDecls are competing sample rates.
	AudioRates []uint32
	// Ladder and SegmentMS configure fan-out and segmentation.
	Ladder    int
	SegmentMS int64
	// CorruptLayer marks the top layer's samples unusable.
	CorruptLayer bool
	// Options
	RequireAllLayers bool
}

// Report is the complete, deterministic outcome of a run.
type Report struct {
	VideoSamples     int     `json:"video_samples"`
	AudioSamples     int     `json:"audio_samples"`
	EventCount       int     `json:"event_count"`
	RecoveredLayers  int     `json:"recovered_layers"`
	TotalLayers      int     `json:"total_layers"`
	BoundaryErrors   int     `json:"boundary_errors"`
	Segments         int     `json:"segments"`
	Keyframes        int     `json:"keyframes"`
	RoundTripMatch   float64 `json:"roundtrip_match"`
	KeyframeHitRate  float64 `json:"keyframe_hit_rate"`
	SegmentHashMatch float64 `json:"segment_hash_match"`
	// UnavailableLayers lists layer indices excluded from the index.
	UnavailableLayers []int `json:"unavailable_layers"`
	// Decisions captures every clock repair decision, ordered by track.
	Decisions []clock.Decision `json:"decisions"`
	// RepairLog is the human-readable list of recoverable and fatal repairs.
	RepairLog []string `json:"repair_log"`
	// Color records the video color resolution.
	Color video.Resolution `json:"color"`
	// Audio records layout and rate resolution.
	AudioLayout audio.Layout       `json:"audio_layout"`
	AudioRate   audio.RateDecision `json:"audio_rate"`
	// SegmentIndexes are the hashed segments of the lowest available layer.
	SegmentIndexes []index.SegmentIndex `json:"segment_indexes"`
	KeyframeIndex  index.KeyframeIndex  `json:"keyframe_index"`
	// Errors holds every classified failure; the first unrecoverable one is
	// returned from Run.
	Errors []*model.Error `json:"errors"`
	// Memory accounting.
	EventMemoryBytes int64 `json:"event_memory_bytes"`
	InputBytes       int64 `json:"input_bytes"`
}

// Recovery is the per-domain recovery view the index stage needs.
type Recovery struct {
	VideoSamples   []model.Sample
	AudioSamples   []model.Sample
	Events         []model.Event
	VideoTrack     model.TrackID
	VideoTimescale uint32
}

// Limits are the hard resource limits from the specification.
type Limits struct {
	MaxInputBytes       int64
	MaxEventMemoryBytes int64
}

// DefaultLimits returns the specified defaults: 2 GiB input, 1 GiB events.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:       2 << 30,
		MaxEventMemoryBytes: 1 << 30,
	}
}

// Run executes the whole pipeline.
func Run(in Input, limits Limits) (*Report, error) {
	if limits.MaxInputBytes == 0 {
		limits = DefaultLimits()
	}
	rep := &Report{TotalLayers: in.Ladder}
	if rep.TotalLayers < 1 {
		rep.TotalLayers = 1
	}
	intake := int64(len(in.MP4))
	for _, p := range in.VideoRTP {
		intake += int64(len(p))
	}
	for _, p := range in.AudioRTP {
		intake += int64(len(p))
	}
	for _, p := range in.ID3 {
		intake += int64(len(p))
	}
	for _, p := range in.Emsg {
		intake += int64(len(p))
	}
	intake += int64(len(in.WebVTT) + len(in.Chapters) + len(in.SEI))
	rep.InputBytes = intake
	if intake > limits.MaxInputBytes {
		e := model.NewError("", model.StageValidation, model.CodeLimitExceeded, false,
			"input is %d bytes, above the %d-byte limit", intake, limits.MaxInputBytes)
		rep.Errors = append(rep.Errors, e)
		return rep, e
	}

	if err := runStages(in, rep, limits); err != nil {
		rep.Errors = append(rep.Errors, classify(err, rep))
		return rep, err
	}
	return rep, nil
}

func classify(err error, rep *Report) *model.Error {
	if pe, ok := model.AsError(err); ok {
		for _, existing := range rep.Errors {
			if existing == pe {
				return pe
			}
		}
		return pe
	}
	return model.WrapError("", model.StageValidation, model.CodeIndexInvalid, false, err, "run failed")
}

// collectError appends a classified error to the report and returns the first
// unrecoverable one as a plain error.
func collectError(rep *Report, err error) error {
	pe, ok := model.AsError(err)
	if !ok {
		return err
	}
	rep.Errors = append(rep.Errors, pe)
	if !pe.Recoverable {
		return pe
	}
	return nil
}

func addErrors(rep *Report, errs []*model.Error) error {
	for _, e := range errs {
		if e == nil {
			continue
		}
		if err := collectError(rep, e); err != nil {
			return err
		}
	}
	return nil
}

// stageContext is the mutable state threaded through the stages.
type stageContext struct {
	in             Input
	limits         Limits
	rep            *Report
	video          interface{}
	videoSamples   []model.Sample
	videoBytes     map[int][]byte
	audioSamples   []model.Sample
	events         []model.Event
	fanoutResult   fanout.Result
	merge          fanin.MergeResult
	videoTimescale uint32
	delivery       ingest.RTPResult
}

func runStages(in Input, rep *Report, limits Limits) error {
	ctx := &stageContext{in: in, limits: limits, rep: rep, videoBytes: map[int][]byte{}}
	for _, stage := range []struct {
		name string
		fn   func(*stageContext) error
	}{
		{"ingest", stageIngest},
		{"clock", stageClock},
		{"recover-video", stageRecoverVideo},
		{"recover-audio", stageRecoverAudio},
		{"recover-events", stageRecoverEvents},
		{"fanout", stageFanout},
		{"fanin", stageFanin},
		{"index", stageIndex},
	} {
		if err := stage.fn(ctx); err != nil {
			return err
		}
	}
	rep.RecoveredLayers = len(ctx.merge.Recovered)
	rep.UnavailableLayers = append([]int(nil), ctx.merge.Unavailable...)
	rep.BoundaryErrors = len(ctx.merge.BoundaryErrors)
	sort.Ints(ctx.rep.UnavailableLayers)
	return nil
}

// ErrorCodeOf extracts the code of err's shared error, or "".
func ErrorCodeOf(err error) string {
	if pe, ok := model.AsError(err); ok {
		return pe.Code
	}
	return ""
}

// ExitCode maps a run outcome to the CLI contract: 0 pass, 2 unrecoverable
// input (including clock, parse, layer and index failures) or boundary
// failure, 1 parameter or runtime failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	code := ErrorCodeOf(err)
	switch code {
	case model.CodeInvalidParameter, model.CodeLimitExceeded:
		return 1
	case model.CodeUnrecoverableLoss, model.CodeBackwardTimestamp, model.CodeClockOverflow,
		model.CodeMalformedPacket, model.CodeMalformedMP4, model.CodeMalformedMetadata,
		model.CodeMissingPayload, model.CodeAudioLayout, model.CodeAudioRate,
		model.CodeAudioGapOverflow, model.CodeBoundaryMismatch, model.CodeIndexInvalid,
		model.CodeLayerUnavailable, model.CodeColorUnknown:
		return 2
	}
	if pe, ok := model.AsError(err); ok && !pe.Recoverable {
		return 2
	}
	return 1
}

// FormatDecisions renders decisions deterministically for the terminal.
func FormatDecisions(decs []clock.Decision) []string {
	out := make([]string, 0, len(decs))
	for _, d := range decs {
		out = append(out, fmt.Sprintf("%s %s raw=%d repaired=%d epoch=%d recoverable=%t %s",
			d.Track, d.Kind, d.RawTicks, d.Repaired, d.Epoch, d.Recoverable, d.Message))
	}
	sort.Strings(out)
	return out
}

// SummarizeError renders the first unrecoverable error compactly.
func SummarizeError(err error) string {
	if err == nil {
		return ""
	}
	var parts []string
	if pe, ok := model.AsError(err); ok {
		parts = append(parts, fmt.Sprintf("track=%s", pe.Track), fmt.Sprintf("stage=%s", pe.Stage), fmt.Sprintf("code=%s", pe.Code))
	}
	parts = append(parts, err.Error())
	return strings.Join(parts, " ")
}
