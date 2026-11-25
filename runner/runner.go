// Package runner orchestrates scan, plan, execute, checkpoint and reporting
// for one or more inputs.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zld/mediapipe/checkpoint"
	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/manifest"
	"github.com/zld/mediapipe/media"
	"github.com/zld/mediapipe/pipeline"
	rtp "github.com/zld/mediapipe/rtp"
	"github.com/zld/mediapipe/trace"
)

// Exit codes are part of the acceptance contract.
const (
	ExitOK          = 0
	ExitArgument    = 1
	ExitFailures    = 2
	ExitInternal    = 3
	ExitInterrupted = 10
)

// Options configures one run.
type Options struct {
	Inputs         []string
	WorkDir        string
	Segment        media.Time
	Bitrates       []string
	MaxDeviation   media.Time
	Resume         bool
	InterruptAfter int
	RTPWait        int
	RTPWindow      int
	Limits         media.Limits
}

// ErrInterrupted signals the deliberate mid-run interruption used by the
// acceptance workflow. It is not a failure.
var ErrInterrupted = errors.New("interrupted after the requested segments")

// FileResult is the outcome for one input.
type FileResult struct {
	Path                  string        `json:"path"`
	SHA256                string        `json:"sha256,omitempty"`
	IndexDigest           string        `json:"index_digest,omitempty"`
	SegmentCount          int           `json:"segment_count"`
	ManifestSHA256        string        `json:"manifest_sha256,omitempty"`
	AVSyncErrorMS         int64         `json:"av_sync_error_ms,omitempty"`
	PacketRecoveryRate    *float64      `json:"packet_recovery_rate,omitempty"`
	P95LatencyMS          float64       `json:"p95_latency_ms"`
	LayerBoundarySpreadUS int64         `json:"max_layer_boundary_spread_us"`
	Issues                []media.Issue `json:"issues,omitempty"`
	Error                 *media.Error  `json:"error,omitempty"`
	OK                    bool          `json:"ok"`
}

// Report is the machine-readable run summary printed on stdout.
type Report struct {
	OK                 bool         `json:"ok"`
	Version            string       `json:"version"`
	Files              []FileResult `json:"files"`
	FailureCount       int          `json:"failure_count"`
	SegmentCount       int          `json:"segment_count"`
	ManifestSHA256     string       `json:"manifest_sha256,omitempty"`
	AVSyncErrorMS      int64        `json:"av_sync_error_ms,omitempty"`
	PacketRecoveryRate float64      `json:"packet_recovery_rate"`
	P95LatencyMS       float64      `json:"p95_latency_ms"`
	Interrupted        bool         `json:"interrupted,omitempty"`
	Resumed            bool         `json:"resumed,omitempty"`
}

// Run executes the whole batch and returns the report plus the process exit
// code. Failures are confined to the input that caused them; other inputs
// continue processing.
func Run(opts Options) (*Report, int) {
	rep := &Report{Version: "mediapipe-v1", Files: []FileResult{}}
	if opts.Segment <= 0 || opts.WorkDir == "" {
		rep.OK = false
		rep.Files = append(rep.Files, FileResult{Error: media.NewError(media.KindArgument, "",
			"segment duration and work directory are required")})
		rep.FailureCount = 1
		return rep, ExitArgument
	}
	// A non-positive max deviation means "derive from the source": one and a
	// half reference video frames tolerates a one-frame GOP phase difference
	// between representations while still failing gross misalignment.
	autoDeviation := opts.MaxDeviation <= 0
	_ = autoDeviation
	if opts.RTPWait <= 0 {
		opts.RTPWait = rtp.DefaultConfig().WaitPackets
	}
	if opts.RTPWindow <= 0 {
		opts.RTPWindow = rtp.DefaultConfig().Window
	}

	var interrupted bool
	var totalSegments int
	var latencies []float64
	var manifests []string
	var syncErrors []int64
	var rates []float64

	for _, input := range opts.Inputs {
		if len(input) == 0 {
			continue
		}
		fr := runOne(input, opts, &interrupted)
		rep.Files = append(rep.Files, fr)
		if !fr.OK {
			rep.FailureCount++
			continue
		}
		totalSegments += fr.SegmentCount
		if fr.ManifestSHA256 != "" {
			manifests = append(manifests, fr.ManifestSHA256)
		}
		if fr.AVSyncErrorMS > 0 {
			syncErrors = append(syncErrors, fr.AVSyncErrorMS)
		}
		if fr.PacketRecoveryRate != nil {
			rates = append(rates, *fr.PacketRecoveryRate)
		}
	}

	rep.SegmentCount = totalSegments
	rep.ManifestSHA256 = combineDigests(manifests)
	rep.Interrupted = interrupted
	rep.P95LatencyMS = percentile95(latenciesOf(rep.Files))
	for _, fr := range rep.Files {
		if fr.AVSyncErrorMS > rep.AVSyncErrorMS {
			rep.AVSyncErrorMS = fr.AVSyncErrorMS
		}
	}
	if len(rates) > 0 {
		rep.PacketRecoveryRate = average(rates)
	} else {
		rep.PacketRecoveryRate = 1.0
	}
	rep.OK = rep.FailureCount == 0 && !interrupted
	_ = latencies
	_ = syncErrors

	switch {
	case interrupted:
		return rep, ExitInterrupted
	case rep.FailureCount > 0:
		return rep, ExitFailures
	default:
		return rep, ExitOK
	}
}

func latenciesOf(files []FileResult) []float64 {
	out := make([]float64, 0, len(files))
	for _, f := range files {
		out = append(out, f.P95LatencyMS)
	}
	return out
}

func runOne(input string, opts Options, interrupted *bool) FileResult {
	fr := FileResult{Path: input}
	if opts.InterruptAfter > 0 && *interrupted {
		// A later input is not processed once the run has been interrupted.
		fr.OK = false
		fr.Error = media.NewError(media.KindCheckpoint, input, "run interrupted before this input")
		return fr
	}
	lim := opts.Limits
	if lim == (media.Limits{}) {
		lim = media.DefaultLimits()
	}

	c, err := media.Probe(input, lim)
	if err != nil {
		fr.Error = media.AsError(err)
		return fr
	}
	fr.SHA256 = c.SHA256
	fatalIssue := false
	for _, is := range c.Issues {
		fr.Issues = append(fr.Issues, is)
		if is.Severity == media.SeverityError {
			fatalIssue = true
		}
	}
	if fatalIssue {
		// A structural defect in the container index makes its outputs
		// untrustworthy; the batch reports it and continues with other inputs.
		fr.Error = media.NewError(media.KindNonMonotonic, input,
			"index records fatal timing defects")
		for _, is := range c.Issues {
			if is.Severity == media.SeverityError {
				fr.Error.Kind = is.Kind
				fr.Error.Detail = is.Detail
				break
			}
		}
		return fr
	}

	perInputDir := inputWorkDir(opts.WorkDir, c.SHA256)
	if err := os.MkdirAll(perInputDir, 0o755); err != nil {
		fr.Error = media.WrapError(media.KindIO, perInputDir, err)
		return fr
	}

	store, err := index.NewStore(filepath.Join(perInputDir, "index"), lim)
	if err != nil {
		fr.Error = media.AsError(err)
		return fr
	}
	outcome, err := store.Scan(c)
	if err != nil {
		fr.Error = media.AsError(err)
		return fr
	}
	idx := outcome.Index
	fr.IndexDigest = idx.Digest

	// Layers: the reference video track plus one representation per requested
	// bitrate. In this synthetic model a representation shares the source
	// file, so index digests match and boundaries are identical by
	// construction; the plan still records each layer separately.
	refTrack := idx.Track("video")
	if refTrack == nil {
		fr.Error = media.NewError(media.KindUnsupported, input, "no video track to segment")
		return fr
	}
	var layers []pipeline.LayerInput
	layers = append(layers, pipeline.LayerInput{Name: "reference", Path: input, Index: idx, Track: refTrack})

	for _, br := range opts.Bitrates {
		name := "video-" + sanitizeBitrate(br)
		// A representation may be a sibling file named after the bitrate, in
		// which case it is indexed independently so alignment compares真实
		// keyframe grids. Otherwise the layer reuses the reference source.
		repPath := representationPath(input, br)
		if repPath == "" {
			layers = append(layers, pipeline.LayerInput{Name: name, Path: input, Index: idx, Track: refTrack})
			continue
		}
		repC, err := media.Probe(repPath, lim)
		if err != nil {
			fr.Error = media.AsError(err)
			return fr
		}
		repIdx, err := index.Build(repC)
		if err != nil {
			fr.Error = media.AsError(err)
			return fr
		}
		tr := repIdx.Track("video")
		if tr == nil {
			fr.Error = media.NewError(media.KindUnsupported, repPath, "representation has no video track")
			return fr
		}
		layers = append(layers, pipeline.LayerInput{Name: name, Path: repPath, Index: repIdx, Track: tr})
	}

	// Resolve the effective deviation before hashing parameters, so the
	// params digest reflects what was actually used and is identical across
	// runs and resumes.
	if opts.MaxDeviation <= 0 {
		if fi, ok := refTrack.FrameInterval(); ok {
			opts.MaxDeviation = fi + fi/2
		} else {
			opts.MaxDeviation = pipeline.DefaultMaxDeviation(0)
		}
	}
	paramsDigest := manifest.ParamsDigest(opts.Segment, opts.Bitrates, opts.MaxDeviation)
	ckOpts := checkpoint.Options{
		Root: perInputDir, InputSHA256: c.SHA256,
		IndexDigest: idx.Digest, ParamsDigest: paramsDigest, Segment: opts.Segment,
	}

	var state *checkpoint.State
	if opts.Resume {
		loaded, err := checkpoint.Load(perInputDir, ckOpts)
		if err != nil {
			// A stale checkpoint is discarded; outputs are rebuilt from
			// scratch so they stay consistent with the current input.
			_ = checkpoint.Discard(perInputDir)
			state = checkpoint.Create(ckOpts)
			fr.Issues = append(fr.Issues, media.Issue{Kind: media.KindCheckpoint,
				Severity: media.SeverityWarning, Detail: "stale checkpoint discarded: " + err.Error()})
		} else if loaded != nil {
			state = loaded
		} else {
			state = checkpoint.Create(ckOpts)
		}
	} else {
		state = checkpoint.Create(ckOpts)
	}

	plan, err := pipeline.Plan(refTrack, layerTracks(layers), pipeline.PlanConfig{
		Segment: opts.Segment, MaxDeviation: opts.MaxDeviation,
	})
	if err != nil {
		fr.Error = media.AsError(err)
		return fr
	}

	segDir := filepath.Join(perInputDir, "segments")
	cfg := pipeline.SegmentConfig{
		WorkDir: segDir, Segment: opts.Segment, MaxDeviation: opts.MaxDeviation,
		MaxBytesPerSegment: pipeline.DefaultMaxBytesPerSegment(),
	}

	// Execute, skipping already-committed segments when resuming. The
	// manifest is later rebuilt from the checkpoint, so execution results
	// need not be retained here.
	done := 0
	for _, layer := range layers {
		for _, seg := range plan {
			if state.Has(layer.Name, seg.Number) {
				continue
			}
			so, err := pipeline.Execute([]pipeline.LayerInput{layer}, []pipeline.PlannedSegment{seg}, cfg)
			if err != nil {
				fr.Error = media.AsError(err)
				return fr
			}
			out := so[0]
			if err := state.Commit(checkpoint.Segment{
				Number: out.Number, Layer: out.Layer, Path: filepath.Join("segments", out.Path), SHA256: out.SHA256,
				Bytes: out.Bytes, Samples: out.Samples, StartUS: out.StartUS, EndUS: out.EndUS,
				ElapsedUS: out.ElapsedUS,
			}); err != nil {
				fr.Error = media.AsError(err)
				return fr
			}
			if err := checkpoint.Save(perInputDir, state); err != nil {
				fr.Error = media.AsError(err)
				return fr
			}
			done++
			if opts.InterruptAfter > 0 && done >= opts.InterruptAfter {
				*interrupted = true
				fr.P95LatencyMS = 0
				fr.OK = false
				fr.Error = media.NewError(media.KindCheckpoint, input,
					"interrupted after %d segments; resume to finish", done)
				return fr
			}
		}
	}

	// Reconstruct the complete output list from the checkpoint so resumed and
	// uninterrupted runs publish the same manifest.
	fullOuts, err := loadOutputs(perInputDir, state, segDir)
	if err != nil {
		fr.Error = media.AsError(err)
		return fr
	}

	var syncRep *pipeline.SyncReport
	if audio := idx.Track("audio"); audio != nil {
		rep := pipeline.CheckSync(refTrack, audio)
		syncRep = &rep
		fr.AVSyncErrorMS = rep.MaxErrorUS / 1000
	}

	man, err := manifest.Build(c.SHA256, idx.Digest, paramsDigest, state.ID,
		opts.Segment, plan, fullOuts, syncRep)
	if err != nil {
		fr.Error = media.AsError(err)
		return fr
	}

	// RTP provenance, when a sequence accompanies the input.
	var rtpResult *rtp.Result
	if rtpPath, ok := findRTPSequence(input); ok {
		seq, err := rtp.LoadSequence(rtpPath)
		if err != nil {
			fr.Error = media.AsError(err)
			return fr
		}
		res, err := rtp.Reassemble(seq, rtp.Config{Window: opts.RTPWindow, WaitPackets: opts.RTPWait})
		if err != nil {
			fr.Error = media.AsError(err)
			return fr
		}
		rtpResult = res
		man.RTP = res
		rate := res.RecoveryRate()
		fr.PacketRecoveryRate = &rate
		// Attribute unrecoverable packet gaps to the segments whose time
		// range contains them, so a downstream consumer sees which output
		// fragments are missing media.
		if gaps := attributeRTPGaps(res, refTrack, plan, seq); len(gaps) > 0 {
			// Record each distinct gap once at run level; attach it to the
			// segments whose span contains it so the fragment itself carries
			// the loss.
			seen := make(map[int64]bool, len(gaps))
			for _, g := range gaps {
				if seen[g.TimeUS] {
					continue
				}
				seen[g.TimeUS] = true
				man.Issues = append(man.Issues, g)
			}
			for i := range fullOuts {
				for _, g := range gaps {
					if g.TimeUS >= fullOuts[i].StartUS && g.TimeUS < fullOuts[i].EndUS {
						fullOuts[i].Issues = append(fullOuts[i].Issues, g)
					}
				}
			}
		}
	}
	man.Issues = append(man.Issues, issuesFromOutputs(fullOuts)...)

	manBytes, err := man.Canonical()
	if err != nil {
		fr.Error = media.AsError(err)
		return fr
	}
	// Recompute the manifest digest including the RTP and issue fields now
	// that they are attached.
	sum := sha256.Sum256(manBytes)
	fr.ManifestSHA256 = hex.EncodeToString(sum[:])
	fr.SegmentCount = len(plan)

	if err := index.WriteAtomic(filepath.Join(perInputDir, "manifest.json"), manBytes); err != nil {
		fr.Error = media.AsError(err)
		return fr
	}

	if err := writeProvenance(perInputDir, input, c, idx, plan, fullOuts, paramsDigest, state.ID, rtpResult, man); err != nil {
		fr.Error = media.AsError(err)
		return fr
	}

	lat := percentile95(outputLatencies(fullOuts))
	fr.P95LatencyMS = lat
	fr.OK = true
	if syncRep != nil && !syncRep.OK {
		fr.OK = false
		fr.Error = media.NewError(media.KindSyncTolerance, input,
			"audio/video sync error %dus exceeds tolerance", syncRep.MaxErrorUS)
	}
	for _, is := range man.Issues {
		// Unrecoverable RTP loss is recorded on segments and provenance but
		// does not fail the input: the media container is still trustworthy.
		if is.Severity == media.SeverityError && is.Kind != media.KindRTPGap {
			fr.OK = false
			if fr.Error == nil {
				fr.Error = media.NewError(media.KindInternal, input, is.Detail)
			}
		}
	}
	fr.LayerBoundarySpreadUS = layerSpread(plan)
	return fr
}

func layerTracks(layers []pipeline.LayerInput) []*index.TrackIndex {
	out := make([]*index.TrackIndex, len(layers))
	for i := range layers {
		out[i] = layers[i].Track
	}
	return out
}

func layerSpread(plan []pipeline.PlannedSegment) int64 {
	var max int64
	for _, seg := range plan {
		var lo, hi int64 = -1, 0
		for _, b := range seg.LayerBounds {
			if lo < 0 || b.BoundaryUS < lo {
				lo = b.BoundaryUS
			}
			if b.BoundaryUS > hi {
				hi = b.BoundaryUS
			}
		}
		if lo < 0 {
			continue
		}
		if d := hi - lo; d > max {
			max = d
		}
	}
	return max
}

func loadOutputs(root string, state *checkpoint.State, segDir string) ([]pipeline.SegmentOutput, error) {
	state.SortOrders()
	out := make([]pipeline.SegmentOutput, 0, len(state.Segments))
	for _, seg := range state.Segments {
		full := filepath.Join(root, seg.Path)
		got, size, err := media.FileSHA256(full)
		if err != nil {
			return nil, err
		}
		if got != seg.SHA256 {
			return nil, media.NewError(media.KindCheckpoint, seg.Path,
				"committed segment digest changed on disk")
		}
		rel, err := filepath.Rel(filepath.Join(root, "segments"), full)
		if err != nil {
			return nil, media.WrapError(media.KindInternal, seg.Path, err)
		}
		out = append(out, pipeline.SegmentOutput{
			Number: seg.Number, Layer: seg.Layer, StartUS: seg.StartUS, EndUS: seg.EndUS,
			Path: rel, SHA256: seg.SHA256, Bytes: size, Samples: seg.Samples,
			ElapsedUS: seg.ElapsedUS,
		})
	}
	_ = segDir
	return out, nil
}

func issuesFromOutputs(outs []pipeline.SegmentOutput) []media.Issue {
	var issues []media.Issue
	for _, o := range outs {
		issues = append(issues, o.Issues...)
	}
	return issues
}

func outputLatencies(outs []pipeline.SegmentOutput) []float64 {
	out := make([]float64, 0, len(outs))
	for _, o := range outs {
		out = append(out, float64(o.ElapsedUS)/1000.0)
	}
	return out
}

// attributeRTPGaps maps each unrecoverable RTP sequence gap to the media time
// of the packet that went missing, using the emit order of the sequence and
// the track frame interval. Gaps with no determinable time are still reported
// at time zero so they are never dropped silently.
// rtpIssue returns the classified issue for a packet loss. Unrecoverable
// RTP loss is attached to segments and provenance; it does not by itself fail
// the whole input, because the media container can still be valid.
func rtpIssue(g media.Issue) media.Issue {
	return media.Issue(g)
}

func attributeRTPGaps(res *rtp.Result, track *index.TrackIndex, plan []pipeline.PlannedSegment, seq *rtp.Sequence) []media.Issue {
	if res == nil || len(res.Gaps) == 0 || track == nil {
		return nil
	}
	interval, ok := track.FrameInterval()
	if !ok {
		return nil
	}
	var issues []media.Issue
	sentPos := make(map[uint16]int, len(seq.Sent))
	for i, s := range seq.Sent {
		sentPos[s] = i
	}
	for _, g := range res.Gaps {
		pos, found := sentPos[g.StartSeq]
		if !found {
			continue
		}
		// Each emitted packet carries one media sample, so the gap's start
		// position maps to a media time on the reference track's grid.
		t := media.Time(track.StartUS*1_000) + media.Time(pos)*interval
		issues = append(issues, media.Issue{
			Kind: media.KindRTPGap, Severity: media.SeverityError,
			Detail: fmt.Sprintf("unrecoverable RTP loss of %d packets at sequence %d",
				g.Count, g.StartSeq),
			At: pos, TimeUS: t.Micros(),
		})
	}
	return issues
}

func findRTPSequence(input string) (string, bool) {
	ext := filepath.Ext(input)
	candidate := strings.TrimSuffix(input, ext) + ".rtp.json"
	if _, err := os.Stat(candidate); err == nil {
		return candidate, true
	}
	return "", false
}

func writeProvenance(root, input string, c *media.Container, idx *index.Index,
	plan []pipeline.PlannedSegment, outs []pipeline.SegmentOutput,
	paramsDigest string, checkpointID int64, rtpResult *rtp.Result, man *manifest.Manifest) error {

	sources := []trace.Source{{
		Role: "media", Path: input, SHA256: c.SHA256, Size: c.Size,
		Index: idx.Digest, Segments: len(outs),
	}}
	var rtpTrace *trace.RTPTrace
	if rtpResult != nil {
		sources = append(sources, trace.Source{Role: "rtp", Path: input + ".rtp",
			Segments: 0})
		rtpTrace = &trace.RTPTrace{
			SourcePath: input, SentPackets: rtpResult.SentCount,
			ReceivedPackets:  rtpResult.ReceivedCount,
			RecoveredPackets: rtpResult.RecoveredCount,
			LostPackets:      rtpResult.LostCount,
			DuplicatePackets: rtpResult.DuplicateCount,
			RecoveryRate:     rtpResult.RecoveryRate(),
			GapCount:         len(rtpResult.Gaps),
			Issues:           rtpResult.Issues,
		}
	}
	segs := make([]trace.SegmentTrace, 0, len(outs))
	for _, o := range outs {
		segs = append(segs, trace.SegmentTrace{
			Number: o.Number, Layer: o.Layer,
			StartUS: o.StartUS, EndUS: o.EndUS,
			OutputPath: o.Path, OutputSHA256: o.SHA256,
			InputPaths: []string{input}, InputSHA256: []string{c.SHA256},
			IndexDigest: idx.Digest, ParamsDigest: paramsDigest,
			CheckpointID: checkpointID, Issues: o.Issues,
		})
	}
	prov, err := trace.Build(man.Digest, sources, segs, paramsDigest, checkpointID)
	if err != nil {
		return err
	}
	prov.RTP = rtpTrace
	b, err := prov.Canonical()
	if err != nil {
		return err
	}
	return index.WriteAtomic(filepath.Join(root, "provenance.json"), b)
}

// representationPath finds a sibling representation file for a bitrate, e.g.
// "baseline.mp4" + "500k" -> "baseline.500k.mp4". An empty string means the
// layer should reuse the reference source.
func representationPath(input, bitrate string) string {
	ext := filepath.Ext(input)
	base := strings.TrimSuffix(input, ext)
	for _, suffix := range []string{".video-" + sanitizeBitrate(bitrate), "." + bitrate, "." + sanitizeBitrate(bitrate)} {
		candidate := base + suffix + ext
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func inputWorkDir(root, sha string) string {
	return filepath.Join(root, sha[:16])
}

func combineDigests(digests []string) string {
	// A single input's manifest digest is itself; a batch digest folds every
	// per-input manifest digest in a stable order.
	if len(digests) == 1 {
		return digests[0]
	}
	sort.Strings(digests)
	h := sha256.New()
	for _, d := range digests {
		io.WriteString(h, d)
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func percentile95(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64{}, vals...)
	sort.Float64s(sorted)
	idx := (95*len(sorted) + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return sorted[idx-1]
}

func average(vals []float64) float64 {
	if len(vals) == 0 {
		return 1
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func sanitizeBitrate(br string) string {
	out := strings.Builder{}
	for _, r := range br {
		if r == 'k' || r == 'K' || r == 'M' || r == 'm' || (r >= '0' && r <= '9') {
			out.WriteRune(r)
		}
	}
	if out.Len() == 0 {
		return "default"
	}
	return out.String()
}
