// Package pipeline orchestrates a batch audit: it derives the unified
// timeline, splits it into fixed chunks, renders frame records per chunk and
// commits each chunk to the checkpoint store.
package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"mediaaudit/internal/checkpoint"
	"mediaaudit/internal/model"
	"mediaaudit/internal/mp4"
	"mediaaudit/internal/render"
	"mediaaudit/internal/report"
	"mediaaudit/internal/subtitle"
	"mediaaudit/internal/timecode"
	"mediaaudit/internal/timeline"
)

// InterruptedError signals the deliberate -interrupt-after stop. It is not a
// failure: the run exits with the recoverable-segment code and the checkpoint
// stays resumable.
type InterruptedError struct {
	After int
}

func (e *InterruptedError) Error() string {
	return fmt.Sprintf("interrupted after %d committed chunks", e.After)
}

// Inputs describe every source file and parameter of the run.
type Inputs struct {
	OutDir string
	// MP4Path is the video sample file.
	MP4Path string
	// Subtitles lists every subtitle file and the track ID it feeds.
	Subtitles []SubtitleInput
	// TimecodePath is a newline-separated timecode list in the given mode.
	TimecodePath string
	TimecodeMode string
	// Width, Height size the synthetic audit raster.
	Width, Height int
	// Interval is the frame sampling cadence in seconds.
	Interval model.Rat
	// SnapTolerance aligns subtitle boundaries to video cuts when non-zero.
	SnapTolerance model.Rat
	// ChunkSize is the number of timeline items per committed chunk.
	ChunkSize int
	// MaxDriftPPM is the per-track drift threshold; negative disables it.
	MaxDriftPPM int64
	// FallbackScan enables the MP4 keyframe scan fallback.
	FallbackScan bool
	// Resume continues from a valid checkpoint; false restarts the batch.
	Resume bool
	// InterruptAfter stops after committing N chunks (0 disables).
	InterruptAfter int
	// VerifyOnly re-checks committed chunks without processing new ones.
	VerifyOnly bool
	// RTPStreams maps a track ID to a generated RTP fixture to audit.
	RTPStreams map[string]string
}

// SubtitleInput is one subtitle file and the track ID it feeds.
type SubtitleInput struct {
	TrackID string
	Path    string
	Offset  model.Rat
}

// Stats is the terminal-facing summary of a run.
type Stats struct {
	Chunks          int
	CommittedChunks int
	// VerifiedChunks counts chunks a resume skipped because they were
	// already committed and passed verification.
	VerifiedChunks int
	// ReplayedChunks counts committed chunks whose work had to be redone.
	// A correct resume never redoes committed work, so this stays zero.
	ReplayedChunks   int
	SegmentHashMatch report.Ratio
	Items            int
	Frames           int
	FrameFailures    int
	Metrics          report.Metrics
	Warnings         int
	Recoverable      int
	ConfigDigest     string
	Manifest         checkpoint.Manifest
	RTP              RTPAudit
	Errors           model.Errors
}

// ExitCode maps the run outcome to the documented process status.
func (s Stats) ExitCode() int {
	if s.Errors.FirstFatal() != nil {
		return 1
	}
	if s.Recoverable > 0 {
		return 2
	}
	return 0
}

// Run executes the batch.
func Run(in Inputs) (Stats, error) {
	var st Stats
	if in.ChunkSize <= 0 {
		return st, model.NewError(model.CodeInvalidArgument, "pipeline", "", model.InputPos{},
			"chunk size must be positive, got %d", in.ChunkSize)
	}
	if in.Interval.Sign() <= 0 {
		return st, model.NewError(model.CodeInvalidArgument, "pipeline", "", model.InputPos{},
			"sampling interval must be positive")
	}
	st.ConfigDigest = configDigest(in)

	video, videoErr := loadVideo(in, &st)
	res := buildTimeline(in, video, &st)
	items := res.Items
	st.Items = len(items)

	store, err := checkpoint.Open(in.OutDir)
	if err != nil {
		return st, err
	}
	pieces := report.Split(items, in.ChunkSize)
	if err := store.Begin(in.ChunkSize, len(pieces), st.ConfigDigest, in.Resume); err != nil {
		return st, err
	}
	st.Chunks = len(pieces)

	type chunkPlan struct {
		index  int
		chunk  report.Chunk
		inputs []string
	}
	plans := make([]chunkPlan, len(pieces))
	for i, its := range pieces {
		c := report.Chunk{Index: i, Items: its}
		c.InputDigest = chunkInputDigest(in, st.ConfigDigest, i, its, video, videoErr)
		plans[i] = chunkPlan{index: i, chunk: c, inputs: []string{c.InputDigest}}
	}

	// Resume: verify committed chunks and render only what is missing. A
	// fresh run renders everything, because Begin has just cleared the store.
	fresh := make([]bool, len(plans))
	if !in.Resume {
		for i := range fresh {
			fresh[i] = true
		}
	}
	if in.Resume {
		for i := range plans {
			meta, ok := store.Meta(i)
			if !ok {
				fresh[i] = true
				continue
			}
			good, verr := store.Verify(i, meta.InputDigest)
			if verr != nil {
				return st, verr
			}
			if !good {
				return st, model.NewError(model.CodeCheckpointCorrupt, "pipeline", "",
					model.InputPos{File: store.ChunkPath(i)}, "chunk %d failed verification", i)
			}
			// The inputs of this run must still hash to what was committed.
			if meta.InputDigest != plans[i].chunk.InputDigest {
				return st, model.NewError(model.CodeCheckpointMismatch, "pipeline", "",
					model.InputPos{File: store.ChunkPath(i)},
					"chunk %d inputs changed since commit; refusing to resume", i)
			}
			p, perr := store.ReadPayload(i)
			if perr != nil {
				return st, perr
			}
			plans[i].chunk = chunkFromPayload(p)
			st.VerifiedChunks++
		}
	}

	reader := newReadAt(video.data)
	track := model.Track{ID: video.trackID, Kind: model.KindVideo}
	opt := render.Options{Interval: in.Interval, Width: in.Width, Height: in.Height}

	committedThisRun := 0
	for i := range plans {
		pl := &plans[i]
		if !fresh[i] {
			// Already committed and verified: count it, do no work. This is
			// what keeps replayed_chunks at zero across a resume.
			continue
		}
		if _, alreadyCommitted := store.Meta(i); alreadyCommitted {
			// Should not happen: Begin(resume) validates the configuration
			// and the loop above validates each committed chunk.
			st.ReplayedChunks++
		}
		if in.VerifyOnly {
			return st, model.NewError(model.CodeCheckpointMismatch, "pipeline", "",
				model.InputPos{}, "chunk %d is not committed but -verify was requested", i)
		}
		if _, alreadyCommitted := store.Meta(i); alreadyCommitted {
			// Only reachable if verification let a committed chunk through
			// but it was not marked fresh; count it as redone work.
			st.ReplayedChunks++
		}
		points := render.SamplingPoints(spansOf(pl.chunk.Items), opt)
		pl.chunk.Frames = render.RenderBatch(reader, track, video.samples, points, opt)
		for _, fr := range pl.chunk.Frames {
			if fr.Error != nil {
				pl.chunk.Errors.Add(fr.Error)
			}
		}
		pl.chunk.Finalize()
		if _, err := store.Commit(pl.chunk, pl.chunk.InputDigest); err != nil {
			return st, err
		}
		committedThisRun++
		// Stop before declaring the batch complete: an interruption must
		// only trigger while work remains, otherwise the run would report a
		// finished batch as interrupted.
		if in.InterruptAfter > 0 && committedThisRun >= in.InterruptAfter && i+1 < len(plans) {
			st.CommittedChunks = store.Committed()
			st.Manifest = store.Manifest()
			return st, &InterruptedError{After: in.InterruptAfter}
		}
	}

	// Collect every committed chunk for the final report and compare the
	// in-memory result with what the store would replay.
	manifest := store.Manifest()
	st.Manifest = manifest
	st.CommittedChunks = len(manifest.Chunks)
	if in.Resume {
		// Only chunks that were missing get rendered; committed ones are
		// verified and skipped. VerifiedChunks reports the work avoided and
		// ReplayedChunks counts any committed chunk that had to be redone.
	}
	matches := 0
	frameFailures := 0
	var itemsAll []timeline.Item
	var framesAll []render.FrameRecord
	for i := range manifest.Chunks {
		p, err := store.ReadPayload(i)
		if err != nil {
			return st, err
		}
		// Frame failures are counted from the committed payloads, whether
		// this process rendered them or verified them on resume. That keeps
		// frame_errors identical between a resume and a fresh run.
		for _, fr := range p.Frames {
			if fr.Error != nil {
				frameFailures++
			}
		}
		// Recompute the chunk digest exactly as the store does: from the
		// persisted items and frames plus the recorded input digest.
		itemsDigest := report.ItemsDigest(p.Items)
		framesDigest := report.FramesDigest(p.Frames)
		if report.InputDigest(itemsDigest, framesDigest, manifest.Chunks[i].InputDigest) == manifest.Chunks[i].Digest {
			matches++
		}
		itemsAll = append(itemsAll, p.Items...)
		framesAll = append(framesAll, p.Frames...)
		for _, e := range p.Errors {
			st.Errors.Add(e)
		}
	}
	st.SegmentHashMatch = report.NewRatio(matches, len(manifest.Chunks))
	st.Frames = len(framesAll)
	st.FrameFailures = frameFailures

	// Round-trip consistency: every timecode label parsed to an instant must
	// render back to the identical label and instant.
	st.Metrics.RoundtripConsistency = timecodeRoundtrip(in, &st)
	// Keyframe hit rate: of the sampling points that produced a frame record,
	// how many resolved their sample through the keyframe index rather than
	// the scan fallback.
	st.Metrics.KeyframeIndexHitRate = keyframeHitRate(framesAll)
	st.Metrics.SegmentHashMatch = st.SegmentHashMatch
	if len(itemsAll) == 0 && len(pieces) == 0 {
		st.SegmentHashMatch = report.NewRatio(0, 0)
	}
	st.Errors = append(st.Errors, res.Errors...)
	// RTP clock audits run after the media batch: a drifting stream fails
	// only its own track and never blocks the committed chunks.
	if len(in.RTPStreams) > 0 {
		st.RTP = AuditRTPStreams(in.RTPStreams, in.MaxDriftPPM)
		for _, e := range st.RTP.Errors {
			st.Errors.Add(e)
		}
	}
	st.Recoverable = st.Errors.RecoverableCount()
	st.Warnings = st.Errors.WarningCount()
	_ = itemsAll
	return st, nil
}

func chunkFromPayload(p checkpoint.Payload) report.Chunk {
	c := report.Chunk{Index: p.Index, Items: p.Items, Frames: p.Frames, Errors: p.Errors}
	c.Finalize()
	return c
}

func spansOf(items []timeline.Item) []model.Span {
	out := make([]model.Span, 0, len(items))
	for _, it := range items {
		if it.Kind == timeline.ItemVideo {
			out = append(out, it.Span)
		}
	}
	if len(out) == 0 {
		for _, it := range items {
			out = append(out, it.Span)
		}
	}
	return out
}

// videoData is the loaded video source and its parsed samples.
type videoData struct {
	data    []byte
	samples []render.SampleRef
	trackID string
}

func loadVideo(in Inputs, st *Stats) (*videoData, *model.Error) {
	v := &videoData{trackID: "v1"}
	if in.MP4Path == "" {
		return v, nil
	}
	data, err := os.ReadFile(in.MP4Path)
	if err != nil {
		e := model.NewError(model.CodeIO, "pipeline", "v1", model.InputPos{File: in.MP4Path},
			"cannot read video: %v", err)
		st.Errors.Add(e)
		return v, e
	}
	v.data = data
	ix, es := mp4.Parse(bytes.NewReader(data), in.MP4Path, mp4.Options{FallbackScan: in.FallbackScan})
	for _, e := range es {
		st.Errors.Add(e)
	}
	tr, ok := ix.VideoTrack()
	if !ok {
		return v, nil
	}
	if tr.KeyframeOrigin == mp4.OriginScan {
		detail := "reason " + tr.FallbackReason
		if tr.BadIndexEntry > 0 {
			detail += fmt.Sprintf(" (entry %d names sample %d of %d)",
				tr.BadIndexEntry, tr.BadIndexSample, tr.SampleCount)
		}
		pos := model.InputPos{File: in.MP4Path}
		if tr.BadIndexOffset > 0 {
			pos.Offset = int64(tr.BadIndexOffset)
		}
		st.Errors.Add(model.NewWarning(model.CodeIndexFallback, "mp4", "v1", pos,
			"keyframe index unusable: %s; scanned sample table for %d samples",
			detail, tr.SampleCount))
	}
	// Presentation order, not decode order: a B-frame clip has ctts offsets
	// that reorder samples, and both sampling lookup and cut points require
	// non-decreasing timestamps.
	raw := ix.Samples(tr.TrackID)
	sort.SliceStable(raw, func(i, j int) bool {
		if c := raw[i].PresentTime.Cmp(raw[j].PresentTime); c != 0 {
			return c < 0
		}
		return raw[i].Index < raw[j].Index
	})
	for _, s := range raw {
		v.samples = append(v.samples, render.SampleRef{
			Index: s.Index, Offset: s.Offset, Size: s.Size,
			Start: s.PresentTime, Duration: s.Duration, Keyframe: s.Keyframe, Origin: string(s.Origin),
		})
	}
	return v, nil
}

// buildTimeline assembles the merge inputs from the parsed sources.
func buildTimeline(in Inputs, v *videoData, st *Stats) timeline.Result {
	b := timeline.BuildOptions{Opts: timeline.Options{Tolerance: in.SnapTolerance}}

	vidTrack := model.Track{ID: v.trackID, Kind: model.KindVideo}
	for _, s := range v.samples {
		b.VideoSamples = append(b.VideoSamples, timeline.Sample{
			Track: vidTrack, Index: s.Index, Start: s.Start, Duration: s.Duration,
			Keyframe: s.Keyframe, Origin: s.Origin, Label: fmt.Sprintf("frame-%d", s.Index),
			SizeBytes: s.Size,
		})
	}
	if len(v.samples) == 0 {
		// A video-less run still gets a synthetic audio-like span so the
		// timeline is non-empty when subtitles exist.
		b.Opts.MaxGap = model.Rat{}
	}

	b.Cues = map[string][]timeline.Cue{}
	for _, si := range in.Subtitles {
		data, err := os.ReadFile(si.Path)
		if err != nil {
			st.Errors.Add(model.NewError(model.CodeIO, "pipeline", si.TrackID,
				model.InputPos{File: si.Path}, "cannot read subtitle: %v", err))
			continue
		}
		format := subtitle.DetectFormat(si.Path, data)
		doc := subtitle.Parse(data, si.Path, format, subtitle.Options{Offset: si.Offset})
		for _, e := range doc.Errors {
			e.Track = si.TrackID
			st.Errors.Add(e)
		}
		// Overlap is reported once per cue pair; both cues still enter the
		// timeline so the valid track is not lost.
		_, ovErrs := subtitle.Overlaps(doc.Cues)
		for _, e := range ovErrs {
			e.Track = si.TrackID
			st.Errors.Add(e)
		}
		track := model.Track{ID: si.TrackID, Kind: model.KindSubtitle}
		b.Subtitles = append(b.Subtitles, track)
		for _, c := range doc.Cues {
			b.Cues[si.TrackID] = append(b.Cues[si.TrackID], timeline.Cue{
				Track: track, Index: c.Index, Start: c.Start, End: c.End,
				Text: c.Text(), Origin: string(format),
			})
		}
	}

	if in.TimecodePath != "" {
		b.Timecodes = loadTimecodes(in, st)
	}

	res := timeline.Build(b)
	return res
}

// loadTimecodes parses one timecode label per line. Blank lines and lines
// starting with '#' are ignored so fixtures can be annotated.
func loadTimecodes(in Inputs, st *Stats) []timeline.TimecodeMark {
	var marks []timeline.TimecodeMark
	mode, err := timecode.ParseModeName(in.TimecodeMode)
	if err != nil {
		st.Errors.Add(err.(*model.Error))
		return nil
	}
	data, rerr := os.ReadFile(in.TimecodePath)
	if rerr != nil {
		st.Errors.Add(model.NewError(model.CodeIO, "pipeline", "tc1",
			model.InputPos{File: in.TimecodePath}, "cannot read timecodes: %v", rerr))
		return nil
	}
	track := model.Track{ID: "tc1", Kind: model.KindTimecode}
	lines := bytes.Split(data, []byte("\n"))
	idx := 0
	for i, raw := range lines {
		line := string(bytes.TrimRight(raw, "\r"))
		if len(bytes.TrimSpace(raw)) == 0 || bytes.HasPrefix(bytes.TrimSpace(raw), []byte("#")) {
			continue
		}
		tc, perr := timecode.Parse(line, mode, model.InputPos{File: in.TimecodePath, Line: i + 1})
		if perr != nil {
			e := perr.(*model.Error)
			e.Track = track.ID
			st.Errors.Add(e)
			continue
		}
		marks = append(marks, timeline.TimecodeMark{
			Track: track, Index: idx, Label: tc.String(), Time: tc.Time, Segment: "tc",
		})
		idx++
	}
	return marks
}

// timecodeRoundtrip measures, over every valid label, the share that parses,
// re-renders canonically and parses back to the identical frame count and
// instant. Invalid labels are excluded: they are reported separately and are
// never silently "fixed" into the ratio.
func timecodeRoundtrip(in Inputs, st *Stats) report.Ratio {
	if in.TimecodePath == "" {
		// No labels means the ratio is not applicable, not perfect.
		return report.NewRatio(0, 0)
	}
	data, err := os.ReadFile(in.TimecodePath)
	if err != nil {
		return report.NewRatio(0, 0)
	}
	mode, ok := timecode.ModeByName(in.TimecodeMode)
	if !ok {
		return report.NewRatio(0, 0)
	}
	good, total := 0, 0
	for i, raw := range bytes.Split(data, []byte("\n")) {
		line := string(bytes.TrimRight(raw, "\r"))
		if len(bytes.TrimSpace(raw)) == 0 || bytes.HasPrefix(bytes.TrimSpace(raw), []byte("#")) {
			continue
		}
		first, perr := timecode.Parse(line, mode, model.InputPos{File: in.TimecodePath, Line: i + 1})
		if perr != nil {
			continue
		}
		total++
		back, rerr := timecode.Parse(first.String(), mode, model.InputPos{})
		if rerr == nil && back.Total == first.Total && back.Time.Equal(first.Time) {
			good++
		}
	}
	return report.NewRatio(good, total)
}

func keyframeHitRate(frames []render.FrameRecord) report.Ratio {
	hit, total := 0, 0
	for _, fr := range frames {
		if fr.Error != nil {
			continue
		}
		total++
		if fr.Origin == string(mp4.OriginIndex) {
			hit++
		}
	}
	return report.NewRatio(hit, total)
}

// configDigest captures every parameter that can change generated digests.
func configDigest(in Inputs) string {
	parts := []string{
		"v" + strconv.Itoa(checkpoint.Version),
		"chunk=" + strconv.Itoa(in.ChunkSize),
		"interval=" + in.Interval.String(),
		"raster=" + strconv.Itoa(in.Width) + "x" + strconv.Itoa(in.Height),
		"snap=" + in.SnapTolerance.String(),
		"drift=" + strconv.FormatInt(in.MaxDriftPPM, 10),
		"mode=" + in.TimecodeMode,
	}
	if in.MP4Path != "" {
		if data, err := os.ReadFile(in.MP4Path); err == nil {
			parts = append(parts, "mp4="+report.Hash(data))
		} else {
			parts = append(parts, "mp4="+filepath.Base(in.MP4Path))
		}
	}
	// Subtitles may arrive in any order; sort their descriptors so the
	// configuration digest depends only on content, not on slice order.
	subParts := make([]string, 0, len(in.Subtitles))
	for _, s := range in.Subtitles {
		if data, err := os.ReadFile(s.Path); err == nil {
			subParts = append(subParts, "sub="+s.TrackID+":"+report.Hash(data))
		} else {
			subParts = append(subParts, "sub="+s.TrackID+":"+filepath.Base(s.Path))
		}
	}
	sort.Strings(subParts)
	parts = append(parts, subParts...)
	if in.TimecodePath != "" {
		if data, err := os.ReadFile(in.TimecodePath); err == nil {
			parts = append(parts, "tc="+report.Hash(data))
		}
	}
	return report.InputDigest(parts...)
}

// chunkInputDigest pins a chunk to its exact timeline slice and sources.
func chunkInputDigest(in Inputs, cfg string, index int, items []timeline.Item, v *videoData, verr *model.Error) string {
	parts := []string{cfg, "chunk=" + strconv.Itoa(index), report.ItemsDigest(items)}
	if verr != nil {
		parts = append(parts, "video_error="+string(verr.Code))
	}
	return report.InputDigest(parts...)
}

// readAt adapts a byte slice to the render.Reader interface.
type readAt []byte

func newReadAt(b []byte) readAt { return readAt(b) }

func (r readAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r)) {
		return 0, io.EOF
	}
	n := copy(p, r[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// ApplySubtitleOverlaps records overlapping cues as recoverable diagnostics
// for a parsed document. It is exported so the CLI can report them before the
// batch starts without duplicating parsing logic.
func ApplySubtitleOverlaps(doc *subtitle.Document, trackID string, st *Stats) {
	cues := make([]subtitle.Cue, len(doc.Cues))
	copy(cues, doc.Cues)
	_, errs := subtitle.Overlaps(cues)
	for _, e := range errs {
		e.Track = trackID
		st.Errors.Add(e)
	}
}
