// Package pipeline wires manifest parsing, transport reordering, media
// inspection, DAG scheduling and checkpointing into one auditable recovery
// run.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strconv"
	"time"

	"archivecheck/internal/checkpoint"
	"archivecheck/internal/fixture"
	"archivecheck/internal/inspect"
	"archivecheck/internal/manifest"
	"archivecheck/internal/model"
)

// Config is one recovery run.
type Config struct {
	// Root is the fixture directory.
	Root string
	// Seed and FaultName identify the injected fixture for the run record.
	Seed      int64
	FaultName string
	// ManifestFile selects the manifest inside Root, relative.
	ManifestFile string
	// StateDir is where the checkpoint lives; empty disables checkpointing.
	StateDir string
	// CheckpointPath, when set, names the checkpoint file exactly and takes
	// precedence over StateDir.
	CheckpointPath string
	// Timebase overrides the manifest timebase when non-zero.
	Timebase int64
	// AllowInexact carries sub-tick durations instead of failing.
	AllowInexact bool
	// FrameTicks is the target frame duration used to merge damage ranges.
	// Zero derives it from each fragment's keyframe structure.
	FrameTicks int64
	// ReorderWindow is the transport reorder window.
	ReorderWindow uint64
	// Resume loads an existing checkpoint and reuses confirmed nodes.
	Resume bool
	// Concurrency bounds DAG parallelism.
	Concurrency int
	// MaxFragmentBytes bounds one fragment's memory footprint.
	MaxFragmentBytes int64
	// Now is injectable for deterministic output.
	Now func() time.Time
}

// DefaultFragmentBytes bounds a single fragment at 64 MiB.
const DefaultFragmentBytes int64 = 64 << 20

// SegmentResult is the per-segment audit record.
type SegmentResult struct {
	Representation string   `json:"representation"`
	ID             string   `json:"id"`
	URI            string   `json:"uri"`
	Start          int64    `json:"start"`
	Duration       int64    `json:"duration"`
	Status         string   `json:"status"`
	Missing        uint64   `json:"missing_packets"`
	Duplicates     int      `json:"duplicates"`
	Reordered      int      `json:"reordered"`
	TrackCount     int      `json:"tracks"`
	SampleCount    int      `json:"samples"`
	KeyframeP50Ms  int64    `json:"keyframe_p50_ms"`
	OutputSHA256   string   `json:"output_sha256"`
	FaultCodes     []string `json:"fault_codes,omitempty"`
	// RepresentationDigestConflict is set when the same RTP sequence number
	// arrived with two different payload digests, which means the retained
	// bytes no longer match the catalog for this layer.
	RepresentationDigestConflict bool `json:"representation_digest_conflict,omitempty"`
}

// Report is the whole run's auditable outcome.
type Report struct {
	Seed             int64                 `json:"seed"`
	Fault            string                `json:"fault"`
	Manifest         string                `json:"manifest"`
	Format           string                `json:"format"`
	Timebase         int64                 `json:"timebase"`
	ManifestSegments int                   `json:"manifest_segments"`
	Representations  int                   `json:"representations"`
	Tracks           int                   `json:"tracks"`
	KeyframeP50Ms    int64                 `json:"keyframe_interval_ms_p50"`
	KeyframeRem      model.Rational        `json:"keyframe_interval_ms_p50_remainder"`
	DamageRanges     []inspect.DamageRange `json:"damage_ranges"`
	MissingPackets   uint64                `json:"missing_packets"`
	DuplicatePackets int                   `json:"duplicate_packets"`
	ReorderedPackets int                   `json:"reordered_packets"`
	FirstBadSegment  string                `json:"first_bad_segment"`
	FirstBadAt       int64                 `json:"first_bad_media_tick,omitempty"`
	Blocked          []BlockedNode         `json:"blocked,omitempty"`
	FirstBlocked     string                `json:"first_blocked_dependency,omitempty"`
	Reps             []RepAudit            `json:"representations_audit"`
	Segments         []SegmentResult       `json:"segments"`
	DAGNodes         int                   `json:"dag_nodes"`
	ExecutedNodes    int                   `json:"executed_nodes"`
	CachedNodes      int                   `json:"cached_nodes"`
	// ResumedNodes counts nodes re-executed because a resume found no
	// confirmation for them; a fully confirmed resume reports 0.
	ResumedNodes int            `json:"resumed_nodes"`
	ResumeMS     int64          `json:"resume_ms"`
	OutputSHA256 string         `json:"output_sha256"`
	Faults       []*model.Fault `json:"faults,omitempty"`
	// CheckpointMismatch is true when a resume was requested but the
	// checkpoint could not be trusted.
	CheckpointMismatch bool `json:"checkpoint_mismatch,omitempty"`
	// CheckpointDiscarded is true when a corrupt checkpoint was dropped and
	// the run continued from scratch instead of aborting.
	CheckpointDiscarded bool `json:"checkpoint_discarded,omitempty"`
	// DurationDeviations lists segments whose media duration differs from the
	// manifest declaration by more than one target frame.
	DurationDeviations []DurationDeviation `json:"duration_deviations,omitempty"`
	ExitCode           int                 `json:"exit_code"`
}

// RepAudit reports representation alignment against the reference layer.
type RepAudit struct {
	ID        string `json:"id"`
	Bandwidth int64  `json:"bandwidth"`
	Timescale int64  `json:"timescale"`
	Segments  int    `json:"segments"`
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	// Aligned is true when the start, end, segment count, per-segment
	// durations and keyframe cadence all match the reference layer within one
	// target frame.
	Aligned bool `json:"aligned"`
	// StartAligned, DurationAligned and KeyframeAligned break out the three
	// alignment dimensions so a report can name which one failed.
	StartAligned    bool  `json:"start_aligned"`
	DurationAligned bool  `json:"duration_aligned"`
	KeyframeAligned bool  `json:"keyframe_aligned"`
	KeyframeP50Ms   int64 `json:"keyframe_p50_ms"`
	// MaxDurationDeviation is the largest per-segment duration difference from
	// the reference layer, in plan ticks.
	MaxDurationDeviation int64 `json:"max_duration_deviation"`
}

// BlockedNode names a node blocked by a failed dependency.
type BlockedNode struct {
	ID         string `json:"id"`
	Dependency string `json:"dependency"`
	Reason     string `json:"reason"`
}

// Run executes the pipeline and returns the report. The returned error is
// reserved for infrastructure problems; analysis faults are in the report.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.MaxFragmentBytes <= 0 {
		cfg.MaxFragmentBytes = DefaultFragmentBytes
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	started := cfg.Now()

	fx, err := fixture.Load(cfg.Root)
	if err != nil {
		return nil, model.NewFault(model.CodeInputMissing, model.SeverityError,
			model.Source{Layer: "pipeline", File: cfg.Root, Offset: -1}, "load fixture index: %v", err).WithCause(err)
	}
	rep := &Report{Seed: fx.Seed, Fault: string(fx.Fault), Format: string(manifest.FormatDASH)}
	if cfg.Seed != 0 {
		rep.Seed = cfg.Seed
	}
	if cfg.FaultName != "" {
		rep.Fault = cfg.FaultName
	}

	// A resume may omit options that shaped the original run; the checkpoint's
	// run record supplies them. This happens before the manifest is chosen so a
	// HLS-only fault resolves to the same playlist.
	var storedRun *checkpoint.RunRecord
	if cfg.Resume && cfg.CheckpointPath != "" {
		if stored, err := checkpoint.NewAt(cfg.CheckpointPath).Load(); err == nil && stored.Run != nil {
			storedRun = stored.Run
			if cfg.Timebase == 0 {
				cfg.Timebase = stored.Run.Timebase
			}
			if cfg.ManifestFile == "" {
				cfg.ManifestFile = stored.Run.Manifest
			}
		}
	}

	mf := cfg.ManifestFile
	if mf == "" {
		mf = defaultManifest(fx)
	}
	rep.Manifest = mf

	plan, err := parseManifest(cfg, fx, mf)
	if err != nil {
		if f, ok := model.AsFault(err); ok {
			rep.Faults = append(rep.Faults, f)
			rep.ExitCode = 2
			rep.finish(cfg, started)
			return rep, nil
		}
		return nil, err
	}
	rep.Format = string(plan.Format)
	rep.Timebase = plan.Timebase
	// Normalise the effective timebase into the run configuration so node
	// parameter digests are identical whether the timebase was derived or
	// explicitly supplied. This is what lets a resume reuse every node.
	cfg.Timebase = plan.Timebase
	// ManifestSegments counts distinct segment IDs on the shared timeline; the
	// per-representation copies are reported separately so the CLI contract's
	// "manifest_segments" is not inflated by the number of layers.
	rep.ManifestSegments = distinctSegmentCount(plan)
	rep.Representations = len(plan.Representations)
	if plan.Faults != nil {
		rep.Faults = append(rep.Faults, plan.Faults.All()...)
	}

	// Prepare the checkpoint store.
	var store *checkpoint.Store
	var entries []checkpoint.Entry
	switch {
	case cfg.CheckpointPath != "":
		store = checkpoint.NewAt(cfg.CheckpointPath)
	case cfg.StateDir != "":
		store = checkpoint.New(cfg.StateDir)
	}
	if store != nil {
		state, err := store.Load()
		if err != nil {
			f, ok := model.AsFault(err)
			if !ok {
				return nil, err
			}
			// A damaged checkpoint cannot be trusted to confirm anything, so
			// recovery proceeds from scratch: only unconfirmed (which is now
			// all) nodes rerun. The fault stays in the report so the audit
			// shows why resume could not be honoured.
			if !cfg.Resume {
				rep.Faults = append(rep.Faults, f)
				rep.CheckpointMismatch = true
				rep.ExitCode = ExitBlocked
				rep.finish(cfg, started)
				return rep, nil
			}
			rep.Faults = append(rep.Faults, f)
			rep.CheckpointMismatch = true
			rep.CheckpointDiscarded = true
			entries = nil
		} else {
			entries = state.Entries
		}
	}

	// Build a task per segment.
	tasks := buildSegmentTasks(cfg, fx, plan, mf)
	// A segment whose media covers a shorter interval than the manifest
	// declares is a duration deviation even if its bytes are intact.
	rep.DurationDeviations = detectDurationDeviations(cfg, tasks)
	runResult, err := runDAG(ctx, cfg, fx, plan, tasks, entries)
	if err != nil {
		return nil, err
	}

	// Assemble the audit from completed segment tasks.
	rep.assemble(fx, plan, runResult, tasks, cfg)

	// Persist the final checkpoint even for damaged or blocked runs so a resume
	// replays every terminal node instead of redoing it.
	if store != nil {
		run := &checkpoint.RunRecord{
			Seed: cfg.Seed, Fault: string(fx.Fault), Manifest: rep.Manifest, Timebase: plan.Timebase,
		}
		if cfg.Resume && storedRun != nil {
			// Keep the recorded configuration on a resume so repeated runs stay
			// self-describing.
			run = storedRun
		}
		if err := store.SaveRun(run, runResult.state.entries); err != nil {
			if f, ok := model.AsFault(err); ok {
				rep.Faults = append(rep.Faults, f)
				rep.ExitCode = 4
				rep.finish(cfg, started)
				return rep, nil
			}
			return nil, err
		}
	}
	rep.finish(cfg, started)
	return rep, nil
}

func parseManifest(cfg Config, fx *fixture.Manifest, rel string) (*manifest.Plan, error) {
	p := path.Join(cfg.Root, rel)
	f, err := os.Open(p)
	if err != nil {
		return nil, model.NewFault(model.CodeInputMissing, model.SeverityError,
			model.Source{Layer: "manifest", File: rel, Offset: -1}, "open manifest: %v", err).WithCause(err)
	}
	defer f.Close()
	opts := manifest.Options{Timebase: cfg.Timebase, AllowInexact: cfg.AllowInexact}
	if isDASH(rel) {
		return manifest.ParseDASH(rel, f, opts)
	}
	if !isHLS(rel) {
		return nil, model.NewFault(model.CodeInputBadArgs, model.SeverityError,
			model.Source{Layer: "manifest", File: rel, Offset: -1}, "unsupported manifest extension")
	}
	return manifest.ParseHLS(rel, f, opts)
}

// distinctSegmentCount counts unique segment IDs across representations.
func distinctSegmentCount(plan *manifest.Plan) int {
	seen := map[string]bool{}
	for _, r := range plan.Representations {
		for _, s := range r.Segments {
			seen[s.ID] = true
		}
	}
	return len(seen)
}

// defaultManifest picks the manifest the fault was designed to exercise. HLS
// only injection faults fall back to the HLS file; everything else uses DASH.
func defaultManifest(fx *fixture.Manifest) string {
	switch fx.Fault {
	case fixture.FaultInexactTimebase, fixture.FaultManifestBoundary:
		return fx.HLSFile
	}
	return fx.DashFile
}

func isDASH(s string) bool { return len(s) > 4 && s[len(s)-4:] == ".mpd" }
func isHLS(s string) bool {
	return len(s) > 5 && (s[len(s)-5:] == ".m3u8" || s[len(s)-4:] == ".m3u")
}

// segmentTask carries everything a segment node needs.
type segmentTask struct {
	representation string
	file           string
	seg            manifest.Segment
	info           fixture.SegmentInfo
	opts           manifest.Options
	timebase       int64
	maxBytes       int64
	window         uint64
	frameTicks     int64
	// rtpTimebase and anchorTS pin the capture's RTP clock to the segment's
	// manifest start so gaps map to media time consistently.
	rtpTimebase int64
	anchorTS    uint32
	// catalogCheck enables the catalog-digest comparison for this run.
	catalogCheck bool
	// rangeStart is the absolute offset of the segment inside its resource
	// when the segment came from a byte-range playlist.
	rangeStart int64
}

// buildSegmentTasks returns tasks in deterministic order.
func buildSegmentTasks(cfg Config, fx *fixture.Manifest, plan *manifest.Plan, manifestFile string) []segmentTask {
	var out []segmentTask
	opts := manifest.Options{Timebase: cfg.Timebase, AllowInexact: cfg.AllowInexact}
	for _, rep := range plan.Representations {
		for _, seg := range rep.Segments {
			info, ok := resolveSegmentInfo(fx, rep, seg)
			if !ok {
				continue
			}
			anchor, rtpBase := captureAnchor(cfg, fx, info)
			// A segment carrying a byte range is read from that range of the
			// shared resource instead of the standalone file, so the report's
			// offsets match what the playlist actually addresses.
			info, rangeStart := applyByteRange(cfg, seg, info)
			if seg.Range != nil {
				info.Init = initRelFor(fx)
			}
			out = append(out, segmentTask{
				representation: rep.ID,
				file:           manifestFile,
				seg:            seg,
				info:           info,
				rangeStart:     rangeStart,
				opts:           opts,
				timebase:       plan.Timebase,
				maxBytes:       cfg.MaxFragmentBytes,
				window:         cfg.ReorderWindow,
				frameTicks:     cfg.FrameTicks,
				rtpTimebase:    rtpBase,
				anchorTS:       anchor,
				catalogCheck:   fx.Fault == fixture.FaultDigestConflict,
			})
		}
	}
	return out
}

// nodeID names a segment node.
func (t segmentTask) nodeID() string { return t.representation + "/" + t.seg.ID }

// captureRTPRange is the sender-declared sequence and timestamp range recorded
// with the fixture, used to detect head and tail loss.
type captureRTPRange struct {
	FirstSeq uint64
	LastSeq  uint64
	FirstTS  uint32
	LastTS   uint32
	AnchorTS uint32
	Timebase int64
	Packets  int
}

// inputDigest folds the segment's declared identity, digests, the capture
// digest and the phase, so reorder and inspect have distinct keys over the same
// source.
func (t segmentTask) inputDigest(phase string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%v",
		phase, t.representation, t.seg.ID, t.seg.URI, t.info.SHA256,
		t.seg.Start, t.seg.Duration, t.timebase, t.opts.AllowInexact)
	return hex.EncodeToString(h.Sum(nil))
}

// paramsDigest folds the tunables that change a phase's output.
func (t segmentTask) paramsDigest(phase string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00v1\x00%d\x00%d\x00%d", phase, t.maxBytes, t.window, t.frameTicks)
	return hex.EncodeToString(h.Sum(nil))
}

// resolveSegmentInfo maps a plan segment onto the fixture's generated media.
// DASH representations match by ID. An HLS media playlist describes the same
// underlying timeline as the fixture's reference representation, so its
// segments resolve by numeric ID against that representation.
func resolveSegmentInfo(fx *fixture.Manifest, rep manifest.Representation, seg manifest.Segment) (fixture.SegmentInfo, bool) {
	if info, ok := fx.SegmentInfoFor(rep.ID, seg.ID); ok {
		return info, true
	}
	if len(fx.Reps) == 0 {
		return fixture.SegmentInfo{}, false
	}
	ref := fx.Reps[0]
	if info, ok := fx.SegmentInfoFor(ref.ID, seg.ID); ok {
		return info, true
	}
	// A byte-range playlist numbers segments 1..N in presentation order; fall
	// back to positional resolution when the ID does not name a segment.
	if idx, err := strconv.Atoi(seg.ID); err == nil && idx >= 1 {
		infos := fx.SegmentsByRep[ref.ID]
		if idx <= len(infos) {
			return infos[idx-1], true
		}
	}
	return fixture.SegmentInfo{}, false
}

// applyByteRange rewrites a segment's media path to the range's resource and
// returns the resource-relative start offset.
func applyByteRange(cfg Config, seg manifest.Segment, info fixture.SegmentInfo) (fixture.SegmentInfo, int64) {
	if seg.Range == nil {
		return info, 0
	}
	info.URI = seg.URI
	info.Size = seg.Range.Len()
	return info, seg.Range.Start
}

// initRelFor returns the reference representation's init file, which describes
// the media shared by a byte-range playlist.
func initRelFor(fx *fixture.Manifest) string {
	if len(fx.Reps) == 0 {
		return ""
	}
	return path.Join(fx.Reps[0].ID, "init.mp4")
}

// captureAnchor reads a capture's declared RTP anchor without decoding the
// payloads, so it is cheap enough to run while planning.
func captureAnchor(cfg Config, fx *fixture.Manifest, info fixture.SegmentInfo) (uint32, int64) {
	raw, err := os.ReadFile(path.Join(cfg.Root, info.Capture))
	if err != nil {
		return 0, fixture.DefaultRTPClock
	}
	c, err := fixture.DecodeCapture(raw)
	if err != nil {
		return 0, fixture.DefaultRTPClock
	}
	return c.AnchorTimestamp, c.RTPTimebase
}
