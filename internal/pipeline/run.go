// Package pipeline wires the ebml, timeline, recover and render stages into
// one deterministic recovery flow: parse -> explicit track graph -> index
// and packet recovery (which depend on the timeline) -> render.
package pipeline

import (
	"archivectl/internal/ebml"
	"archivectl/internal/fixture"
	"archivectl/internal/recover"
	"archivectl/internal/render"
	"archivectl/internal/timeline"
)

// Limits enforced across every run, per the recovery constraints.
const (
	MaxFileBytes    = int64(2) << 30 // 2 GiB
	MaxTracks       = timeline.MaxTracks
	MaxLossWindow   = recover.MaxRecoveryWindow
)

// StageError is a structured error tagged with the stage that produced it.
type StageError struct {
	Stage    string
	Category string
	Offset   int64
	Message  string
}

func (e StageError) Error() string {
	return e.Stage + ": " + e.Category + ": " + e.Message
}

// Result is the complete, deterministic output of one pipeline run.
type Result struct {
	Frames        []render.Frame
	FrameHashes   []render.FrameHash
	ManifestSHA   string
	Index         *recover.Index
	Merged        *timeline.Merged
	Graph         *timeline.Graph
	Audio         []render.AudioStats
	RTP           *recover.RTPResult
	Errors        []StageError
	AVSyncP95Ms   float64
	PipelineP95Ms float64
	SampleCount   int
}

// Options configures one run.
type Options struct {
	Seed       int64
	LossWindow int
	Render     render.FrameOptions
}

// DefaultOptions returns sane bounded defaults.
func DefaultOptions() Options {
	return Options{
		Seed:       42,
		LossWindow: 8,
		Render:     render.DefaultFrameOptions(),
	}
}

// Run executes the full pipeline on a synthetic fixture set plus an RTP
// capture. The same options always produce the same frame hashes and counts.
func Run(set *fixture.Set, opts Options) (*Result, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	res := &Result{}

	// Stage 1: parse every EBML variant, collecting structured errors.
	docs := map[string]*ebml.Document{
		"normal":     ebml.ParseDocument(set.Normal),
		"unknown":    ebml.ParseDocument(set.Unknown),
		"truncated":  ebml.ParseDocument(set.Truncated),
		"corrupt":    ebml.ParseDocument(set.Corrupt),
		"incomplete": ebml.ParseDocument(set.Incomplete),
	}
	for name, doc := range docs {
		for _, e := range doc.Errors {
			res.Errors = append(res.Errors, StageError{
				Stage: "ebml/" + name, Category: e.Category,
				Offset: e.Offset, Message: e.Message,
			})
		}
	}

	// Stage 2: build the explicit track graph from the healthy document and
	// convert every recovered block into timeline samples.
	graph, samples := buildGraphAndSamples(docs)
	if err := graph.Resolve(); err != nil {
		res.Errors = append(res.Errors, StageError{
			Stage: "timeline", Category: err.Category, Message: err.Message,
		})
	}
	res.Graph = graph
	merged := timeline.Merge(graph, samples, timeline.DefaultMergeOptions())
	for _, e := range merged.Errors {
		res.Errors = append(res.Errors, StageError{
			Stage: "timeline", Category: e.Category, Offset: e.Offset,
			Message: e.Message,
		})
	}
	for _, e := range timeline.CheckInRange(merged) {
		res.Errors = append(res.Errors, StageError{
			Stage: "timeline", Category: e.Category, Message: e.Message,
		})
	}
	res.Merged = merged
	res.SampleCount = len(merged.Samples)

	// Stage 3: index and RTP recovery both depend on the parsed timeline.
	res.Index = recover.RebuildIndex(docs["truncated"])
	res.AVSyncP95Ms = percentile(avSyncGapsMs(merged), 95)
	packets, _, err := fixture.DecodeRTPCapture(set.RTP)
	if err != nil {
		return nil, StageError{Stage: "rtp", Category: "malformed_capture",
			Message: err.Error()}
	}
	rtpRes, err := recover.RecoverRTP(packets, opts.LossWindow)
	if err != nil {
		return nil, StageError{Stage: "rtp", Category: "window_config",
			Message: err.Error()}
	}
	res.RTP = rtpRes

	// Stage 4: render frames sampled from the merged timeline, then hash.
	points := renderPoints(merged)
	start, end := merged.Clock.MinNs, merged.Clock.MaxNs
	frames, err := render.RenderFrames(points, start, end, opts.Render)
	if err != nil {
		return nil, StageError{Stage: "render", Category: "raster",
			Message: err.Error()}
	}
	res.Frames = frames
	res.FrameHashes = render.HashFrames(frames)
	res.ManifestSHA = render.ManifestDigest(res.FrameHashes)
	res.Audio = render.SummarizeAudio(audioPoints(merged))
	return res, nil
}

// validateOptions enforces the hard configuration limits and exit semantics.
func validateOptions(opts Options) error {
	if opts.LossWindow <= 0 || opts.LossWindow > MaxLossWindow {
		return StageError{Stage: "config", Category: "invalid_loss_window",
			Message: "loss window must be in 1..256"}
	}
	return nil
}
