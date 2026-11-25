package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"rtpregress/internal/media"
	"rtpregress/internal/planner"
	"rtpregress/internal/timeline"
	"rtpregress/internal/verify"
	"rtpregress/internal/worker"
)

// stageDocVersion tags the on-disk stage documents. A resumed run that finds
// a different version refuses rather than misreading old bytes.
const stageDocVersion = 1

// lateDoc is the stage-file form of a late RTP packet.
type lateDoc struct {
	Sequence   uint64 `json:"sequence"`
	Seq16      uint16 `json:"seq16"`
	FirstStage string `json:"firstStage"`
	Position   int    `json:"position"`
	Duplicate  bool   `json:"duplicate"`
}

// rtpDoc is the persisted RTP accounting of one trace.
type rtpDoc struct {
	Present       bool      `json:"present"`
	Track         string    `json:"track"`
	Committed     int       `json:"committed"`
	PermanentLoss int       `json:"permanentLoss"`
	FrontierLoss  int       `json:"frontierLoss"`
	Duplicates    int       `json:"duplicates"`
	Late          []lateDoc `json:"late"`
}

// parseDoc is the parse stage's committed output: every metadata event on the
// nanosecond timeline plus the per-track timebases it came from.
type parseDoc struct {
	Version       int                  `json:"version"`
	Sample        string               `json:"sample"`
	InputBytes    int                  `json:"inputBytes"`
	MVHDTimescale uint32               `json:"mvhdTimescale"`
	Events        []timeline.Event     `json:"events"`
	Tracks        []timeline.TrackInfo `json:"tracks"`
	RTP           rtpDoc               `json:"rtp"`
}

// alignDoc is the align stage's committed output. Each event keeps both its
// original and snapped time, so the verifier can measure the true deviation
// without trusting the generator.
type alignDoc struct {
	Version        int                     `json:"version"`
	Events         []timeline.AlignedEvent `json:"events"`
	MaxDeviationNs int64                   `json:"maxDeviationNs"`
	Segments       int                     `json:"segments"`
	Starts         []int64                 `json:"starts"`
	Counts         []int                   `json:"counts"`
}

// generateDoc is the generate stage's committed output: where the artifact
// went and how large it is. The artifact bytes themselves live beside it.
type generateDoc struct {
	Version  int    `json:"version"`
	Artifact string `json:"artifact"`
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256"`
	Segments int    `json:"segments"`
}

// runContext carries the deterministic inputs and the selected plan. Stage
// functions only use it to locate files; every fact they persist is derived
// from the input bytes, the plan or an earlier stage's committed output, so a
// resumed process reconstructs exactly the same outputs.
type runContext struct {
	in        *inputs
	plan      *planner.Plan
	dir       string
	mp4Path   string
	tracePath string
	o         *options
}

func newRunContext(in *inputs, plan *planner.Plan, dir, mp4Path, tracePath string, o *options) *runContext {
	return &runContext{in: in, plan: plan, dir: dir, mp4Path: mp4Path, tracePath: tracePath, o: o}
}

// stages returns the four stage functions keyed by name.
func (rc *runContext) stages() map[string]worker.StageFunc {
	return map[string]worker.StageFunc{
		worker.StageParse:    rc.stageParse,
		worker.StageAlign:    rc.stageAlign,
		worker.StageGenerate: rc.stageGenerate,
		worker.StageVerify:   rc.stageVerify,
	}
}

func (rc *runContext) parseDoc() parseDoc {
	d := parseDoc{
		Version:       stageDocVersion,
		Sample:        filepath.Base(rc.mp4Path),
		InputBytes:    rc.in.inputBytes,
		MVHDTimescale: rc.in.mvhdScale,
		Tracks:        rc.in.tracks,
	}
	d.Events = append(d.Events, rc.in.events...)
	if re := rc.in.reorder; re != nil {
		d.RTP = rtpDoc{
			Present:       true,
			Track:         rc.in.trace.Header.Track,
			Committed:     len(re.Packets),
			PermanentLoss: re.PermanentlyLost(),
			FrontierLoss:  re.FrontierGaps().Total(),
			Duplicates:    re.Duplicates,
		}
		for _, late := range re.Late {
			d.RTP.Late = append(d.RTP.Late, lateDoc{
				Sequence: late.Sequence, Seq16: late.SequenceNumber,
				FirstStage: late.FirstStage, Position: late.Position,
				Duplicate: late.Duplicate,
			})
		}
		if d.RTP.Late == nil {
			d.RTP.Late = []lateDoc{}
		}
	}
	return d
}

// stageParse commits the normalized event list.
func (rc *runContext) stageParse(in worker.StageInput) (worker.StageOutput, error) {
	data, err := marshalDoc(rc.parseDoc())
	if err != nil {
		return worker.StageOutput{}, media.At(err, media.Loc{Stage: worker.StageParse})
	}
	return worker.StageOutput{Bytes: data}, nil
}

// stageAlign reads the committed parse output and snaps every event to the
// plan's precision grid. It deliberately does not consult process memory, so
// a resumed run gets the same alignment as a continuous one.
func (rc *runContext) stageAlign(in worker.StageInput) (worker.StageOutput, error) {
	var pd parseDoc
	if err := readStage(in, worker.StageParse, &pd); err != nil {
		return worker.StageOutput{}, err
	}
	if pd.Version != stageDocVersion {
		return worker.StageOutput{}, media.At(media.Newf(media.KindResume,
			"parse stage output version %d, want %d", pd.Version, stageDocVersion),
			media.Loc{Stage: worker.StageAlign})
	}
	events := append([]timeline.Event(nil), pd.Events...)
	// The artifact stores times as unsigned nanoseconds. A negative time is
	// a timebase anomaly, not something to wrap or silently shift, so it
	// fails here with the originating track and byte offset.
	for i, e := range events {
		if e.TimeNs < 0 {
			return worker.StageOutput{}, media.At(media.Newf(media.KindTimebase,
				"event %d has negative media time %dns", i+1, e.TimeNs),
				media.Loc{Track: e.Track, Sample: e.Sample, Stage: worker.StageAlign,
					Offset: e.Offset, HasOffset: e.HasOffset})
		}
	}
	al, err := timeline.Align(events, rc.plan)
	if err != nil {
		return worker.StageOutput{}, media.At(err, media.Loc{Stage: worker.StageAlign})
	}
	doc := alignDoc{
		Version:        stageDocVersion,
		Events:         al.Events,
		MaxDeviationNs: al.MaxDeviationNs,
		Segments:       al.Segments,
		Counts:         al.SegmentCounts,
	}
	for i := 0; i < al.Segments; i++ {
		doc.Starts = append(doc.Starts, int64(i)*int64(rc.plan.SegmentMS)*1_000_000)
	}
	data, err := marshalDoc(doc)
	if err != nil {
		return worker.StageOutput{}, media.At(err, media.Loc{Stage: worker.StageAlign})
	}
	return worker.StageOutput{Bytes: data}, nil
}

// stageGenerate encodes the artifact and writes it atomically. The stage
// output records the artifact's identity; the bytes are the same on every
// re-execution because Encode is a pure function of the aligned events.
func (rc *runContext) stageGenerate(in worker.StageInput) (worker.StageOutput, error) {
	var ad alignDoc
	if err := readStage(in, worker.StageAlign, &ad); err != nil {
		return worker.StageOutput{}, err
	}
	if ad.Version != stageDocVersion {
		return worker.StageOutput{}, media.At(media.Newf(media.KindResume,
			"align stage output version %d, want %d", ad.Version, stageDocVersion),
			media.Loc{Stage: worker.StageGenerate})
	}
	events := make([]verify.WriteEvent, 0, len(ad.Events))
	for _, e := range ad.Events {
		events = append(events, verify.WriteEvent{
			TimeNs: e.QuantizedNs, Segment: e.Segment,
			Track: e.Track, Source: string(e.Source), Text: e.Text,
		})
	}
	segs := make([]verify.Segment, len(ad.Starts))
	for i, start := range ad.Starts {
		count := 0
		if i < len(ad.Counts) {
			count = ad.Counts[i]
		}
		segs[i] = verify.Segment{StartNs: start, EventCount: count}
	}
	data, err := verify.Encode(events, segs, verify.WriteParams{
		SegmentMS: rc.plan.SegmentMS, PrecisionUS: rc.plan.PrecisionUS,
	})
	if err != nil {
		return worker.StageOutput{}, media.At(media.Newf(media.KindVerify,
			"encode artifact: %v", err), media.Loc{Stage: worker.StageGenerate})
	}
	if err := worker.AtomicWrite(artifactPath(rc.dir), data); err != nil {
		return worker.StageOutput{}, err
	}
	meta := generateDoc{
		Version: stageDocVersion, Artifact: filepath.Base(artifactPath(rc.dir)),
		Bytes: len(data), SHA256: verify.SHA256(data), Segments: len(segs),
	}
	out, err := marshalDoc(meta)
	if err != nil {
		return worker.StageOutput{}, media.At(err, media.Loc{Stage: worker.StageGenerate})
	}
	return worker.StageOutput{Bytes: out}, nil
}

// stageVerify re-reads the artifact from disk and checks it against the
// committed alignment plus the operator's constraints.
func (rc *runContext) stageVerify(in worker.StageInput) (worker.StageOutput, error) {
	var gen generateDoc
	if err := readStage(in, worker.StageGenerate, &gen); err != nil {
		return worker.StageOutput{}, err
	}
	var ad alignDoc
	if err := readStage(in, worker.StageAlign, &ad); err != nil {
		return worker.StageOutput{}, err
	}
	art, err := verify.Read(filepath.Join(in.Dir, gen.Artifact))
	if err != nil {
		return worker.StageOutput{}, err
	}
	if art.Size != gen.Bytes || art.FileSHA256 != gen.SHA256 {
		return worker.StageOutput{}, media.At(media.Newf(media.KindVerify,
			"artifact %s is %d bytes %s, generate committed %d bytes %s",
			gen.Artifact, art.Size, art.FileSHA256, gen.Bytes, gen.SHA256),
			media.Loc{Stage: worker.StageVerify, Offset: 0, HasOffset: true})
	}
	res, err := verify.Check(art, rc.expected(&ad, rc.plan, rc.o))
	if err != nil {
		return worker.StageOutput{}, err
	}
	if !res.OK {
		return worker.StageOutput{}, mismatchError(res.Mismatch)
	}
	out, err := marshalDoc(res)
	if err != nil {
		return worker.StageOutput{}, media.At(err, media.Loc{Stage: worker.StageVerify})
	}
	return worker.StageOutput{Bytes: out}, nil
}

// expected derives the verifier's expectations from the committed alignment
// and the selected plan.
func (rc *runContext) expected(ad *alignDoc, plan *planner.Plan, o *options) verify.Expected {
	exp := verify.Expected{
		Events:         len(ad.Events),
		MaxDeviationNs: plan.QuantizeDeviationNs(),
		MaxBytes:       o.maxBytes,
		QualityMin:     o.qualityMin,
		Quality:        plan.Quality,
		SegmentMS:      plan.SegmentMS,
		PrecisionUS:    plan.PrecisionUS,
		Segments:       plan.Segments,
	}
	for _, e := range ad.Events {
		exp.ExpectedTimes = append(exp.ExpectedTimes, e.TimeNs)
	}
	return exp
}

func mismatchError(m *verify.Mismatch) error {
	return media.At(media.Newf(media.KindVerify, "%s", m.Error()), media.Loc{
		Track: m.Track, Stage: worker.StageVerify,
		Offset: m.Offset, HasOffset: m.HasOffset,
	})
}

// loadArtifact re-reads the delivered artifact after the stage loop.
func (rc *runContext) loadArtifact() (*verify.Artifact, error) {
	return verify.Read(artifactPath(rc.dir))
}

// loadAlign re-reads the committed alignment for the final report.
func (rc *runContext) loadAlign() (*alignDoc, error) {
	raw, err := os.ReadFile(filepath.Join(rc.dir, "stage-"+worker.StageAlign+".out"))
	if err != nil {
		return nil, media.At(media.Newf(media.KindResume, "read align stage output: %v", err),
			media.Loc{Stage: worker.StageVerify})
	}
	var ad alignDoc
	if err := json.Unmarshal(raw, &ad); err != nil {
		return nil, media.At(media.Newf(media.KindResume, "decode align stage output: %v", err),
			media.Loc{Stage: worker.StageVerify})
	}
	if ad.Version != stageDocVersion {
		return nil, media.At(media.Newf(media.KindResume,
			"align stage output version %d, want %d", ad.Version, stageDocVersion),
			media.Loc{Stage: worker.StageVerify})
	}
	return &ad, nil
}

// summary is the run's stable machine-readable result. It is rebuilt from
// the parsed inputs and the plan, never from timing or loop state, so a
// resumed run publishes the same summary as a continuous one.
func (rc *runContext) summary() worker.RunSummary {
	s := worker.RunSummary{
		Sample:       filepath.Base(rc.mp4Path),
		Output:       filepath.Base(artifactPath(rc.dir)),
		Events:       len(rc.in.events),
		BySource:     map[string]int{},
		InputBytes:   rc.in.inputBytes,
		Quality:      rc.plan.Quality,
		Segments:     rc.plan.Segments,
		StageIndexes: map[string]int{},
		Plan: map[string]any{
			"bitrateKbps":    rc.plan.BitrateKbps,
			"precisionUs":    rc.plan.PrecisionUS,
			"segmentMs":      rc.plan.SegmentMS,
			"quality":        rc.plan.Quality,
			"costMs":         rc.plan.CostMS,
			"estimatedBytes": rc.plan.EstimatedBytes,
		},
	}
	for _, e := range rc.in.events {
		s.BySource[string(e.Source)]++
	}
	var maxDev int64
	for _, e := range rc.in.events {
		dev := rc.plan.QuantizeNs(e.TimeNs) - e.TimeNs
		if dev < 0 {
			dev = -dev
		}
		if dev > maxDev {
			maxDev = dev
		}
	}
	s.MaxDeviationMs = float64(maxDev) / 1e6
	for i, name := range worker.Stages {
		s.StageIndexes[name] = i
	}
	if re := rc.in.reorder; re != nil {
		s.RTPCommitted = len(re.Packets)
		s.RTPLoss = re.PermanentlyLost()
		s.RTPDuplicate = re.Duplicates
		s.RTPLate = len(re.Late)
		stages := map[string]bool{}
		for _, late := range re.Late {
			stages[late.FirstStage] = true
		}
		for name := range stages {
			s.LateStages = append(s.LateStages, name)
		}
		sort.Strings(s.LateStages)
	}
	return s
}

// readStage decodes one committed stage document.
func readStage(in worker.StageInput, name string, dst any) error {
	raw, err := in.PriorOutput(name)
	if err != nil {
		return media.At(media.Newf(media.KindResume, "read %s stage output: %v", name, err),
			media.Loc{Stage: in.Stage})
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return media.At(media.Newf(media.KindResume, "decode %s stage output: %v", name, err),
			media.Loc{Stage: in.Stage})
	}
	return nil
}

// marshalDoc renders a stage document deterministically. encoding/json sorts
// map keys and struct fields are fixed, so identical values give identical
// bytes.
func marshalDoc(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal stage document: %w", err)
	}
	return append(data, '\n'), nil
}
