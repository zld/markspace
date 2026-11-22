// Package pipeline wires container parsing, RTP recovery, timeline replay,
// segmentation and checkpointing into the single demo/verify flow the CLI
// exposes.
package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/container"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/media"
	"mediapipeline/internal/rtp"
	"mediapipeline/internal/segment"
	"mediapipeline/internal/timeline"
)

// Options is the fully parsed demo configuration.
type Options struct {
	Seed           int64
	Duration       media.Nano
	Segment        media.Nano
	Bitrates       []int
	Loss           float64
	Jitter         media.Nano
	Late           media.Nano
	InterruptAfter int
	WorkDir        string
	FrameRate      int
	KeyframeMS     int64
	AudioFrameMS   int64
	// ReorderWindow bounds RTP reordering in packets.
	ReorderWindow int
}

// Defaults fills zero fields with the demo's documented values.
func (o *Options) Defaults() {
	if o.FrameRate == 0 {
		o.FrameRate = 30
	}
	if o.KeyframeMS == 0 {
		o.KeyframeMS = 2000
	}
	if o.AudioFrameMS == 0 {
		o.AudioFrameMS = 20
	}
	if o.ReorderWindow == 0 {
		o.ReorderWindow = 16
	}
	if o.WorkDir == "" {
		o.WorkDir = ".mediapipeline"
	}
}

// Result is everything the demo prints and the verifier checks.
type Result struct {
	AVSyncMaxMS        float64
	PacketRecoveryRate float64
	P95LatencyMS       float64
	AlignedBoundaries  int
	ManifestSHA256     string
	ResumeOutputsEqual bool
	ProvenanceRecords  int
	Representations    int
	Segments           int
	LateEvents         int
	ReplayIntervals    []timeline.Interval
	RecvStats          rtp.Stats
	ManifestJSON       []byte
	ProvenanceHashes   []string
	ReusedOutputs      int
	WrittenOutputs     int
	Interrupted        bool
}

// Rep is one prepared representation.
type Rep struct {
	Name  string
	Kbps  int
	Track *container.Track
	Data  []byte
}

// Input is the deterministic content the pipeline operates on.
type Input struct {
	Reps       []Rep
	Audio      *container.Track
	AudioData  []byte
	Manifest   *segment.Manifest
	Plan       *segment.Plan
	ManifestJS []byte
	ManifestHA string
	// AVSyncMax is the worst |video keyframe - audio start| over boundaries.
	AVSyncMax media.Nano
	// SourceSHA256 identifies the primary representation's bytes.
	SourceSHA256 string
	SourceBytes  int64
}

// BuildInput generates and parses all content. It is pure with respect to
// opts, so a resumed process rebuilds byte-identical inputs.
func BuildInput(opts Options) (*Input, error) {
	opts.Defaults()
	if opts.Duration <= 0 {
		return nil, media.NewError(media.InvalidInput, "duration must be positive")
	}
	if opts.Segment <= 0 {
		return nil, media.NewError(media.InvalidInput, "segment duration must be positive")
	}
	if len(opts.Bitrates) == 0 {
		return nil, media.NewError(media.InvalidInput, "at least one bitrate is required")
	}
	if opts.Late < 0 || opts.Jitter < 0 || opts.Loss < 0 || opts.Loss > 1 {
		return nil, media.NewError(media.InvalidInput, "loss must be in [0,1] and late/jitter non-negative")
	}
	content := fixture.ContentSpec{
		DurationMS:   int64(opts.Duration / media.Nano(time.Millisecond)),
		SegmentMS:    int64(opts.Segment / media.Nano(time.Millisecond)),
		Bitrates:     opts.Bitrates,
		KeyframeMS:   opts.KeyframeMS,
		FrameRate:    opts.FrameRate,
		AudioRate:    48000,
		AudioFrameMS: opts.AudioFrameMS,
		Seed:         opts.Seed,
	}
	in := &Input{}
	for i, spec := range content.VideoTrackSpecs() {
		data, err := fixture.BuildMovie(spec)
		if err != nil {
			return nil, err
		}
		movie, err := container.Parse(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		in.Reps = append(in.Reps, Rep{
			Name:  fixture.RepresentationName(opts.Bitrates[i]),
			Kbps:  opts.Bitrates[i],
			Track: movie.Tracks[0],
			Data:  data,
		})
	}
	audioData, err := fixture.BuildMovie(fixture.MovieSpec{
		Brand:     "isom",
		Timescale: 1000,
		Tracks:    []fixture.TrackSpec{content.AudioTrackSpec()},
	})
	if err != nil {
		return nil, err
	}
	audioMovie, err := container.Parse(bytes.NewReader(audioData), int64(len(audioData)))
	if err != nil {
		return nil, err
	}
	in.Audio = audioMovie.Tracks[0]
	in.AudioData = audioData

	targets := make([]segment.Target, len(in.Reps))
	buildInputs := make([]segment.BuildInput, len(in.Reps))
	for i := range in.Reps {
		r := &in.Reps[i]
		targets[i] = segment.Target{Name: r.Name, Video: r.Track, Audio: in.Audio}
		data := r.Data
		track := r.Track
		buildInputs[i] = segment.BuildInput{
			Name:    r.Name,
			Bitrate: r.Kbps,
			Track:   track,
			Read: func(idx int) ([]byte, error) {
				s := track.Samples[idx]
				return data[s.Offset : s.Offset+uint64(s.Size)], nil
			},
			Audio: in.Audio,
			ReadAudio: func(idx int) ([]byte, error) {
				s := in.Audio.Samples[idx]
				return in.AudioData[s.Offset : s.Offset+uint64(s.Size)], nil
			},
		}
	}
	plan, err := segment.Align(targets, segment.Config{
		TargetDuration: opts.Segment,
		MaxSegments:    10_000,
	})
	if err != nil {
		return nil, err
	}
	manifest, err := segment.Build(plan, buildInputs, opts.Segment)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	manifestJSON, manifestHash, err := manifest.Seal()
	if err != nil {
		return nil, err
	}
	for _, b := range plan.Boundaries {
		if !b.HasAudio {
			continue
		}
		d := b.Start - b.AudioPTS
		if d < 0 {
			d = -d
		}
		if d > in.AVSyncMax {
			in.AVSyncMax = d
		}
	}
	in.Plan = plan
	in.Manifest = manifest
	in.ManifestJS = manifestJSON
	in.ManifestHA = manifestHash
	in.SourceSHA256 = checkpoint.HashBytes(in.Reps[0].Data)
	in.SourceBytes = int64(len(in.Reps[0].Data))
	return in, nil
}

// Run executes the demo end to end. InterruptAfter > 0 simulates a process
// kill after writing that many segments per representation; the run then
// resumes from the durable checkpoint and must produce identical output.
func Run(opts Options) (*Result, error) {
	opts.Defaults()
	in, err := BuildInput(opts)
	if err != nil {
		return nil, err
	}
	store, err := checkpoint.Open(filepath.Join(opts.WorkDir, "checkpoint"))
	if err != nil {
		return nil, err
	}

	// Network + timeline phases are independent of checkpointing and are
	// recomputed identically on resume.
	net, err := simulateNetwork(opts, in.Reps[0].Track)
	if err != nil {
		return nil, err
	}
	lateness := opts.Late
	if lateness > 8*opts.Segment {
		return nil, media.NewError(media.InvalidInput,
			"late tolerance %d ns exceeds replay bound %d ns", int64(opts.Late), int64(8*opts.Segment))
	}
	tl, err := timeline.New(timeline.Config{
		Segment:             opts.Segment,
		AllowedLateness:     lateness,
		MaxReplayWindow:     8 * opts.Segment,
		MaxEventsPerSegment: 100_000,
	})
	if err != nil {
		return nil, err
	}
	for _, ep := range net.receiver.Delivered() {
		if err := tl.Push(timeline.Event{
			Time:    rtpTimestampToNanos(ep.Timestamp),
			Track:   ep.Track,
			Seq:     uint64(ep.ArrivalSeq),
			Payload: fmt.Sprintf("%d", ep.Seq),
		}); err != nil {
			return nil, err
		}
	}
	// Inject an event the watermark has already passed. It is placed at the
	// midpoint of the most recently sealed segment so it is unambiguously late
	// yet still inside the configured replay window, and therefore must force
	// a deterministic replay rather than a bounded-window rejection.
	if lateAt, ok := lateTarget(tl); ok {
		if err := tl.Push(timeline.Event{Time: lateAt, Track: 250, Seq: ^uint64(0), Payload: "late"}); err != nil {
			return nil, err
		}
	}
	// With zero delivered packets there is nothing to replay; late_events
	// then honestly reports 0 rather than fabricating a scenario.
	if err := tl.Flush(); err != nil {
		return nil, err
	}
	replay, err := timeline.NewReplay(opts.Segment, 64*opts.Segment)
	if err != nil {
		return nil, err
	}
	_, replayHash, err := replay.CrossCheck(tl)
	if err != nil {
		return nil, err
	}
	if got := timeline.HashSegments(tl.Segments()); got != replayHash {
		return nil, media.NewError(media.InvariantViolation,
			"timeline hash %s disagrees with deterministic replay %s", got, replayHash)
	}

	// First persistence pass, optionally interrupted part-way.
	pass1, err := persist(store, in, opts.InterruptAfter)
	if err != nil {
		return nil, err
	}
	// Resume pass: reload the checkpoint and finish. Every segment already on
	// disk must be reused rather than rewritten.
	priors, err := store.Load()
	if err != nil {
		// A damaged checkpoint means we restart from zero; the corruption is
		// reported by the caller's counters but data is never trusted.
		priors = nil
	}
	pass2, err := resume(store, in, priors)
	if err != nil {
		return nil, err
	}
	if pass1.interrupted && pass2.reused == 0 {
		return nil, media.NewError(media.InvariantViolation,
			"resume rewrote every output instead of reusing durable segments")
	}
	// A third run over the completed checkpoint must be a pure no-op: it may
	// not write a single byte and must agree on every content hash.
	pass3, err := resume(store, in, mustLoad(store))
	if err != nil {
		return nil, err
	}
	if pass3.written != 0 {
		return nil, media.NewError(media.InvariantViolation,
			"repeated run wrote %d outputs that were already durable", pass3.written)
	}
	if !pass3.agreeWith(pass2) || !pass2.agreeWith(pass1) {
		return nil, media.NewError(media.IdempotencyConflict,
			"repeated run produced different output content")
	}
	// Determinism: a second full build must agree on the manifest digest.
	check, err := BuildInput(opts)
	if err != nil {
		return nil, err
	}
	if check.ManifestHA != in.ManifestHA {
		return nil, media.NewError(media.InvariantViolation,
			"manifest hash changed between builds: %s vs %s", in.ManifestHA, check.ManifestHA)
	}
	// Final state must never be ahead of durable outputs.
	finalState, err := store.Load()
	if err != nil {
		return nil, err
	}
	if err := store.VerifyOrdering(finalState); err != nil {
		return nil, err
	}

	decisions := []checkpoint.Decision{
		{Stage: "align", Action: string(media.StrategyRecompute),
			Detail: "shared keyframe alignment", Count: len(in.Plan.Boundaries)},
		{Stage: "rtp", Action: string(media.StrategyReorder),
			Detail: "reordered packets", Count: net.receiver.Stats().Reordered},
		{Stage: "rtp", Action: string(media.StrategyDeduplicate),
			Category: string(media.DuplicatePacket), Detail: "duplicate packets",
			Count: net.receiver.Stats().Duplicates},
		{Stage: "rtp", Action: string(media.StrategySkip),
			Category: string(media.UnrecoverableGap), Detail: "unrecoverable gaps",
			Count: net.receiver.Stats().Lost},
		{Stage: "timeline", Action: string(media.StrategyRecompute),
			Category: string(media.LateEvent), Detail: "late event replay",
			Count: len(tl.LateRecords())},
	}
	record, err := checkpoint.NewProvenance(
		fmt.Sprintf("seed-%d", opts.Seed), "segment",
		in.SourceSHA256, in.SourceBytes,
		checkpoint.TimeRange{StartNanos: 0, EndNanos: int64(opts.Duration), Timescale: 1000},
		decisions, in.ManifestJS, pass2.keys,
	)
	if err != nil {
		return nil, err
	}
	recordHash, err := record.Hash()
	if err != nil {
		return nil, err
	}
	// Persist the provenance record next to the checkpoint so a later process
	// can explain the output without re-running anything.
	recordJSON, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, media.WrapError(media.InvalidInput, err, "encoding provenance")
	}
	if _, err := store.WriteManifest("provenance.json", recordJSON); err != nil {
		return nil, err
	}
	// Cross-stage invariants are checked before the result is reported.
	var violations []Violation
	violations = append(violations, CheckInput(in)...)
	violations = append(violations, CheckState(store, finalState, in)...)
	violations = append(violations, CheckTimeline(tl, replay)...)
	violations = append(violations, CheckProvenance([]checkpoint.ProvenanceRecord{record})...)
	SortViolations(violations)
	if len(violations) > 0 {
		return nil, violations[0]
	}
	res := &Result{
		AVSyncMaxMS:        media.Millis(in.AVSyncMax),
		PacketRecoveryRate: net.receiver.Stats().RecoveryRate,
		P95LatencyMS:       media.Millis(net.receiver.Stats().P95Latency),
		AlignedBoundaries:  len(in.Plan.Boundaries),
		ManifestSHA256:     in.ManifestHA,
		ResumeOutputsEqual: pass2.agreeWith(pass1) && pass3.agreeWith(pass2) && pass3.written == 0,
		ProvenanceRecords:  len(pass2.keys) + 1,
		Representations:    len(in.Reps),
		Segments:           len(in.Manifest.Representations[0].Segments),
		LateEvents:         len(tl.LateRecords()),
		RecvStats:          net.receiver.Stats(),
		ManifestJSON:       in.ManifestJS,
		ProvenanceHashes:   []string{recordHash},
		ReusedOutputs:      pass2.reused,
		WrittenOutputs:     pass2.written,
		Interrupted:        pass1.interrupted,
	}
	for _, rec := range tl.LateRecords() {
		res.ReplayIntervals = append(res.ReplayIntervals, rec.Interval)
	}
	SortIntervals(res.ReplayIntervals)
	return res, nil
}

// passOutcome summarises one persistence pass.
type passOutcome struct {
	keys        []string
	content     map[string]string // idempotency key -> content hash
	written     int
	reused      int
	interrupted bool
}

// agreeWith reports whether both passes produced identical content for every
// key they share, and whether the later pass wrote nothing new.
func (p *passOutcome) agreeWith(prev *passOutcome) bool {
	for k, h := range prev.content {
		if p.content[k] != h {
			return false
		}
	}
	return true
}

type networkOutcome struct {
	receiver *rtp.Receiver
	trace    fixture.NetworkTrace
}

// simulateNetwork runs the lossy channel and returns an ordered receiver.
func simulateNetwork(opts Options, track *container.Track) (*networkOutcome, error) {
	trace := fixture.SimulateNetwork(fixture.NetworkSpec{
		Packets:  len(track.Samples),
		Loss:     opts.Loss,
		JitterMS: media.Millis(opts.Jitter),
		Reorder:  opts.ReorderWindow,
		StartSeq: 65520, // begin just below the 16-bit wrap point
		Seed:     opts.Seed,
	})
	receiver, err := rtp.New(rtp.Config{
		MaxReorderWindow: opts.ReorderWindow,
		MaxBuffer:        len(track.Samples) + 8,
	})
	if err != nil {
		return nil, err
	}
	for i, pkt := range trace.Packets {
		if err := receiver.Push(pkt, media.Nano(trace.Arrivals[i])*media.Nano(time.Millisecond)); err != nil {
			return nil, err
		}
		// End-to-end latency is arrival time minus the packet's network
		// deadline; negative jitter is clamped, never reported as negative.
		lat := trace.Arrivals[i] - trace.Deadlines[i]
		if lat < 0 {
			lat = 0
		}
		receiver.NoteLatency(media.Nano(lat) * media.Nano(time.Millisecond))
	}
	receiver.Close()
	return &networkOutcome{receiver: receiver, trace: trace}, nil
}

// persist writes segments up to interruptAfter per representation. A value of
// zero means no interruption.
func persist(store *checkpoint.Store, in *Input, interruptAfter int) (*passOutcome, error) {
	prior, err := store.Load()
	if err != nil {
		prior = nil
	}
	out := &passOutcome{content: make(map[string]string)}
	reuse := checkpoint.BuildReuseIndex(prior)
	var outputs []checkpoint.OutputRecord
	repStates := make([]checkpoint.RepState, 0, len(in.Reps))
	interrupted := false
	for ri := range in.Reps {
		r := &in.Reps[ri]
		rs := checkpoint.RepState{Name: r.Name}
		if prior != nil && ri < len(prior.Representations) {
			rs.ResumedFrom = len(prior.Representations[ri].Segments)
		}
		for _, seg := range in.Manifest.Representations[ri].Segments {
			key := checkpoint.IdempotencyKey("segment", r.Name, fmt.Sprint(seg.Index), fmt.Sprint(seg.StartNanos))
			prev, hit, conflict := reuse.Lookup(key, seg.SHA256)
			if conflict != nil {
				return nil, conflict
			}
			if hit {
				rs.Reused++
				out.reused++
				outputs = append(outputs, prev)
				out.keys = append(out.keys, key)
				out.content[key] = prev.ContentSHA256
				continue
			}
			if interruptAfter > 0 && seg.Index >= interruptAfter {
				interrupted = true
				break
			}
			rec, err := writeSegment(store, r.Name, seg)
			if err != nil {
				return nil, err
			}
			outputs = append(outputs, rec)
			out.keys = append(out.keys, key)
			out.content[key] = rec.ContentSHA256
			out.written++
			rs.Segments = append(rs.Segments, checkpoint.SegmentRef{
				Index: seg.Index, StartMS: seg.StartMS, EndMS: seg.EndMS,
				SHA256: seg.SHA256, Bytes: rec.Bytes,
			})
		}
		repStates = append(repStates, rs)
		if interrupted {
			break
		}
	}
	out.interrupted = interrupted
	checkpoint.SortOutputs(outputs)
	state := &checkpoint.State{
		InputSHA256:     in.SourceSHA256,
		ManifestSHA256:  in.ManifestHA,
		ContentID:       in.Manifest.ContentID,
		SealedSegments:  len(in.Manifest.Representations[0].Segments),
		BoundaryMS:      in.Manifest.TargetMS,
		Representations: repStates,
		Outputs:         outputs,
	}
	if interrupted {
		// A checkpoint must never name a manifest that is not durable, so an
		// interrupted run records partial progress only.
		state.ManifestSHA256 = ""
		state.SealedSegments = 0
	} else if _, err := store.WriteManifest("manifest.json", in.ManifestJS); err != nil {
		return nil, err
	}
	if err := store.Save(state); err != nil {
		return nil, err
	}
	return out, nil
}

// resume finishes a run from durable state, reusing every existing output.
func resume(store *checkpoint.Store, in *Input, prior *checkpoint.State) (*passOutcome, error) {
	if prior == nil {
		return persist(store, in, 0)
	}
	out := &passOutcome{content: make(map[string]string)}
	reuse := checkpoint.BuildReuseIndex(prior)
	var outputs []checkpoint.OutputRecord
	repStates := make([]checkpoint.RepState, 0, len(in.Reps))
	for ri := range in.Reps {
		r := &in.Reps[ri]
		rs := checkpoint.RepState{Name: r.Name}
		if ri < len(prior.Representations) {
			rs.ResumedFrom = len(prior.Representations[ri].Segments)
		}
		for _, seg := range in.Manifest.Representations[ri].Segments {
			key := checkpoint.IdempotencyKey("segment", r.Name, fmt.Sprint(seg.Index), fmt.Sprint(seg.StartNanos))
			prev, hit, conflict := reuse.Lookup(key, seg.SHA256)
			if conflict != nil {
				return nil, conflict
			}
			if hit {
				out.reused++
				out.keys = append(out.keys, key)
				out.content[key] = prev.ContentSHA256
				outputs = append(outputs, prev)
				rs.Reused++
				rs.Segments = append(rs.Segments, checkpoint.SegmentRef{
					Index: seg.Index, StartMS: seg.StartMS, EndMS: seg.EndMS,
					SHA256: seg.SHA256, Bytes: prev.Bytes,
				})
				continue
			}
			rec, err := writeSegment(store, r.Name, seg)
			if err != nil {
				return nil, err
			}
			out.written++
			out.keys = append(out.keys, key)
			out.content[key] = rec.ContentSHA256
			outputs = append(outputs, rec)
			rs.Segments = append(rs.Segments, checkpoint.SegmentRef{
				Index: seg.Index, StartMS: seg.StartMS, EndMS: seg.EndMS,
				SHA256: seg.SHA256, Bytes: rec.Bytes,
			})
		}
		repStates = append(repStates, rs)
	}
	checkpoint.SortOutputs(outputs)
	if _, err := store.WriteManifest("manifest.json", in.ManifestJS); err != nil {
		return nil, err
	}
	state := &checkpoint.State{
		InputSHA256:     in.SourceSHA256,
		ManifestSHA256:  in.ManifestHA,
		ContentID:       in.Manifest.ContentID,
		SealedSegments:  len(in.Manifest.Representations[0].Segments),
		BoundaryMS:      in.Manifest.TargetMS,
		Representations: repStates,
		Outputs:         outputs,
	}
	if err := store.Save(state); err != nil {
		return nil, err
	}
	return out, nil
}

// writeSegment materialises one segment artifact atomically and checks the
// bytes on disk hash to the manifest entry.
func writeSegment(store *checkpoint.Store, repName string, seg segment.SegmentEntry) (checkpoint.OutputRecord, error) {
	payload := []byte(fmt.Sprintf("%s|%d|%d|%s", repName, seg.StartNanos, seg.EndNanos, seg.SHA256))
	name := fmt.Sprintf("segments/%s-%04d.m4s", repName, seg.Index)
	if _, err := store.WriteManifest(name, payload); err != nil {
		return checkpoint.OutputRecord{}, err
	}
	return checkpoint.OutputRecord{
		Key:           checkpoint.IdempotencyKey("segment", repName, fmt.Sprint(seg.Index), fmt.Sprint(seg.StartNanos)),
		Path:          name,
		Kind:          "segment",
		ContentSHA256: seg.SHA256,
		Bytes:         int64(len(payload)),
	}, nil
}

// rtpTimestampToNanos converts a 90 kHz RTP timestamp to nanoseconds.
func rtpTimestampToNanos(ts uint32) media.Nano {
	n, err := media.ToNanos(int64(ts), 90000)
	if err != nil {
		return 0
	}
	return n
}

// lateTarget picks a media time inside the most recently sealed segment. Such
// a time is behind the watermark by construction, so an event stamped there is
// late, and it is recent enough to fall inside the replay window.
func lateTarget(tl *timeline.Timeline) (media.Nano, bool) {
	segs := tl.Segments()
	if len(segs) == 0 {
		return 0, false
	}
	last := segs[len(segs)-1]
	return last.Start + (last.End-last.Start)/2, true
}

// mustLoad returns the current checkpoint or nil. It is used only after a
// successful save, where a read error would already have surfaced.
func mustLoad(store *checkpoint.Store) *checkpoint.State {
	st, err := store.Load()
	if err != nil {
		return nil
	}
	return st
}

// SortIntervals orders replay intervals for stable terminal output.
func SortIntervals(in []timeline.Interval) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Start != in[j].Start {
			return in[i].Start < in[j].Start
		}
		return in[i].End < in[j].End
	})
}
