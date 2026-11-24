// Package engine wires the kernel stages into one deterministic pipeline.
//
// Accepted capture events are kept in an event-sourced log. The delivered
// result is a pure function of that log: per-track entries are classified for
// loss, drift-corrected, sorted by their own timestamps and digested. The
// jitter buffer and watermark drive live lateness detection and checkpoint
// boundaries; because output is always re-derived from the accepted log, a
// rewind triggered by a late event, or a resume from a checkpoint, yields the
// same delivered records as an uninterrupted run.
package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

	"mediasync/internal/checkpoint"
	"mediasync/internal/manifest"
	"mediasync/internal/model"
	"mediasync/internal/output"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
	"mediasync/internal/timeline"
)

// Default limit values, applied by Defaults.
const (
	DefaultWrapBits       = 16
	DefaultTsWrapBits     = 32
	DefaultDriftPPM       = 200
	DefaultLateWindowMS   = 500
	DefaultReorder        = 64
	DefaultMaxPackets     = 1 << 16
	DefaultMaxSamples     = 1 << 18
	DefaultMaxReplay      = 1 << 18
	DefaultMaxCheckpoints = 1 << 12
)

// Options are kernel parameters. Every bound is explicit so memory use is
// predictable; all paths stay inside WorkDir.
type Options struct {
	WorkDir         string
	Seed            int64
	WrapBits        uint
	TsWrapBits      uint
	DriftPPM        float64
	LateWindowMS    float64
	ReorderPackets  int
	MaxPackets      int
	MaxReplay       int
	MaxCheckpoints  int
	CheckpointEvery int64
	InterruptAfter  int64
}

// Defaults fills unset values with documented defaults.
func (o *Options) Defaults() {
	if o.WrapBits == 0 {
		o.WrapBits = DefaultWrapBits
	}
	if o.TsWrapBits == 0 {
		o.TsWrapBits = DefaultTsWrapBits
	}
	if o.DriftPPM == 0 {
		o.DriftPPM = DefaultDriftPPM
	}
	if o.LateWindowMS == 0 {
		o.LateWindowMS = DefaultLateWindowMS
	}
	if o.ReorderPackets == 0 {
		o.ReorderPackets = DefaultReorder
	}
	if o.MaxReplay == 0 {
		o.MaxReplay = DefaultMaxReplay
	}
	if o.MaxCheckpoints == 0 {
		o.MaxCheckpoints = DefaultMaxCheckpoints
	}
	if o.CheckpointEvery == 0 {
		o.CheckpointEvery = 1000
	}
}

// Validate rejects unusable configurations.
func (o Options) Validate() error {
	loc := model.Loc{}
	if o.WorkDir == "" {
		return model.NewError(model.CodeManifestInvalid, loc, "work directory must be set")
	}
	if o.WrapBits == 0 || o.WrapBits > 63 {
		return model.NewError(model.CodeManifestInvalid, loc, "wrap bits must be in 1..63, got %d", o.WrapBits)
	}
	if o.TsWrapBits == 0 || o.TsWrapBits > 63 {
		return model.NewError(model.CodeManifestInvalid, loc, "timestamp wrap bits must be in 1..63, got %d", o.TsWrapBits)
	}
	if o.DriftPPM <= 0 || o.DriftPPM > 10_000 {
		return model.NewError(model.CodeManifestInvalid, loc, "drift ppm must be in (0,10000], got %g", o.DriftPPM)
	}
	if o.LateWindowMS < 0 {
		return model.NewError(model.CodeManifestInvalid, loc, "late window must not be negative")
	}
	if o.ReorderPackets < 0 {
		return model.NewError(model.CodeManifestInvalid, loc, "reorder packets must not be negative")
	}
	if o.CheckpointEvery < 0 {
		return model.NewError(model.CodeManifestInvalid, loc, "checkpoint interval must not be negative")
	}
	if o.InterruptAfter < 0 {
		return model.NewError(model.CodeManifestInvalid, loc, "interrupt point must not be negative")
	}
	return nil
}

// Input is a capture log plus the manifest describing its tracks.
type Input struct {
	Manifest *manifest.Manifest
	Events   []model.PacketEvent
	// EventBytes is the byte offset of each event line, parallel to Events.
	EventBytes []int64
}

// LoadInput reads a manifest and an NDJSON capture log.
func LoadInput(manifestPath, eventsPath string) (*Input, error) {
	man, err := manifest.ParseFile(manifestPath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil, model.NewError(model.CodeManifestSyntax, model.Loc{File: eventsPath}, "open capture events").Wrap(err)
	}
	defer f.Close()
	in := &Input{Manifest: man}
	br := bufio.NewReaderSize(f, 1<<20)
	var off int64
	var errs model.ErrorList
	for {
		line, rerr := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var ev model.PacketEvent
			dec := json.NewDecoder(bytes.NewReader(line))
			dec.DisallowUnknownFields()
			if derr := dec.Decode(&ev); derr != nil {
				errs.Add(model.NewError(model.CodeCheckpointCorrupt,
					model.Loc{File: eventsPath, ByteOffset: off, HasByteOffset: true}, "invalid capture event %d", len(in.Events)).Wrap(derr))
			} else {
				in.Events = append(in.Events, ev)
				in.EventBytes = append(in.EventBytes, off)
			}
		}
		off += int64(len(line))
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: eventsPath}, "read capture events").Wrap(rerr)
		}
	}
	if len(errs) > 0 {
		return in, errs
	}
	return in, nil
}

// LateEvent is a late arrival retained for the final report.
type LateEvent struct {
	TrackID     uint32      `json:"track"`
	ExtSeq      int64       `json:"ext_seq"`
	Corrected   model.Nanos `json:"corrected_ns"`
	AffectedAt  model.Nanos `json:"affected_ns"`
	RewindIndex int         `json:"rewind_index"`
	Recomputed  int64       `json:"recomputed"`
}

// Result is the complete kernel output.
type Result struct {
	Summary          output.Summary
	Records          []output.Record
	Errors           model.ErrorList
	Digest           string
	LostByTrack      map[uint32]int64
	RecoveredByTrack map[uint32]int64
	LateRecomputes   int64
	LateEvents       []LateEvent
	CheckpointCount  int
	ResumedFrom      int64
	ResumeMS         float64
	FallbackReason   string
	MaxSkew          model.Nanos
}

// Engine owns per-run state. A single engine runs one input to completion.
type Engine struct {
	opts     Options
	man      *manifest.Manifest
	tracks   map[uint32]manifest.Track
	order    []uint32
	dedupers map[uint32]*rtp.Deduper
	tsExt    map[uint32]*rtp.TimestampExtender
	est      *sync.Estimator
	corr     *sync.Corrector
	store    *checkpoint.Store
	replay   *timeline.Replay

	accepted       []accepted
	repairs        []adminRepair
	late           []LateEvent
	lateRec        int64
	packets        int64
	dups           int64
	bases          map[uint32]model.Nanos
	processed      int64
	interrupted    bool
	Errors         model.ErrorList
	checkpoints    int
	resumedFrom    int64
	resumeMS       float64
	fallbackReason string
	// wm is the live event-time watermark driving lateness detection.
	wm timeline.Watermark
	// points is the bounded set of rewind markers, oldest first.
	points     []*marker
	lastRecord *checkpoint.Record
}

// accepted is one media packet retained in the event log.
type accepted struct {
	Event    int64           `json:"event"`
	TrackID  uint32          `json:"track"`
	Kind     model.TrackKind `json:"kind"`
	Keyframe bool            `json:"keyframe"`
	Ext      int64           `json:"ext"`
	Raw      model.Nanos     `json:"raw_ns"`
	Arrival  model.Nanos     `json:"arrival_ns"`
	Size     int             `json:"size"`
}

// adminRepair is one out-of-band redundancy record.
type adminRepair struct {
	Event   int64   `json:"event"`
	TrackID uint32  `json:"track"`
	Source  int64   `json:"source"`
	Targets []int64 `json:"targets"`
}

// marker is a rewind marker: the accepted-log length, dedupe state and the
// watermark at the moment it was taken.
type marker struct {
	Events    int64                            `json:"events"`
	Accepted  int                              `json:"accepted"`
	Repairs   int                              `json:"repairs"`
	Watermark model.Nanos                      `json:"watermark"`
	Started   bool                             `json:"started"`
	Dedup     map[string]rtp.DeduperSnapshot   `json:"dedup"`
	Ts        map[string]rtp.TimestampSnapshot `json:"ts"`
}

// New constructs an engine for a manifest.
func New(opts Options, man *manifest.Manifest) (*Engine, error) {
	opts.Defaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if man == nil {
		return nil, model.NewError(model.CodeManifestInvalid, model.Loc{}, "manifest is nil")
	}
	e := &Engine{
		opts:     opts,
		man:      man,
		tracks:   map[uint32]manifest.Track{},
		bases:    map[uint32]model.Nanos{},
		dedupers: map[uint32]*rtp.Deduper{},
		tsExt:    map[uint32]*rtp.TimestampExtender{},
	}
	for _, t := range man.Tracks {
		if _, dup := e.tracks[t.ID]; dup {
			return nil, model.NewError(model.CodeManifestDuplicate, model.Loc{File: man.Path(), TrackID: t.ID, Field: "id"}, "duplicate track id")
		}
		e.tracks[t.ID] = t
		e.order = append(e.order, t.ID)
		d, err := rtp.NewDeduper(opts.WrapBits, 0)
		if err != nil {
			return nil, err
		}
		e.dedupers[t.ID] = d
		tse, err := rtp.NewTimestampExtender(opts.TsWrapBits, t.Timescale)
		if err != nil {
			return nil, err
		}
		e.tsExt[t.ID] = tse
	}
	est, err := sync.NewEstimator(sync.Config{MaxPPM: opts.DriftPPM, Window: 256, MinSamples: 8})
	if err != nil {
		return nil, err
	}
	e.est = est
	e.corr = sync.NewCorrector(est)
	lat := model.Nanos(opts.LateWindowMS * float64(model.NanosPerMilli))
	wm, err := timeline.NewWatermark(timeline.Config{AllowedLateness: lat, MaxSamples: DefaultMaxSamples})
	if err != nil {
		return nil, err
	}
	e.wm = *wm
	rep, err := timeline.NewReplay(opts.MaxReplay)
	if err != nil {
		return nil, err
	}
	e.replay = rep
	st, err := checkpoint.NewStore(opts.WorkDir, opts.MaxCheckpoints)
	if err != nil {
		return nil, err
	}
	e.store = st
	return e, nil
}
