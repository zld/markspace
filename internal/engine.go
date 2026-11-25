package continuity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The engine drives one end-to-end run: load manifests, demux init and media
// segments through a bounded queue, map tracks, advance the timeline, remux and
// emit a stable report. All state is local; nothing is fetched.

// FaultKind selects an injected acceptance scenario.
type FaultKind string

const (
	FaultNone           FaultKind = ""
	FaultTruncatedBox   FaultKind = "truncated-box"
	FaultLateCheckpoint FaultKind = "late-with-checkpoint"
	FaultBadReference   FaultKind = "bad-reference"
)

// EngineConfig parameterises a run.
type EngineConfig struct {
	Root       string
	Format     []ManifestKind
	LateWindow Ticks
	QueueBytes int64
	Fault      FaultKind
	// WorkDir receives checkpoint state for the recovery scenario.
	WorkDir string
}

// Report is the stable terminal and machine summary.
type Report struct {
	Samples         int
	LateRecomputed  int
	LateRejected    int
	TimeDeltaTicks  int64
	ByteDelta       int64
	MappingMissing  int
	FailureLocation string
	SHA256          string
	TrackMapDigest  string
	Formats         []ManifestKind
	Segments        int
	Bytes           int64
	Gaps            int
	Duplicates      int
	OverlapsDropped int
	Recovered       bool
	// Tracks lists the output target tracks.
	Tracks []TargetTrack
	// MappingLines documents source-to-target track mapping.
	MappingLines []string
	// Lines is the human-facing per-sample provenance log.
	Lines []string
}

func (r *Report) ensure() {
	if r.FailureLocation == "" {
		r.FailureLocation = "none"
	}
}

type sourceRun struct {
	label    string
	manifest *Manifest
	// manifests holds every successfully parsed format for cross-checking.
	manifests map[ManifestKind]*Manifest
}

// Run executes the engine for cfg. Errors map to the CLI exit contract.
func Run(ctx context.Context, cfg EngineConfig) (*Report, error) {
	if len(cfg.Format) == 0 {
		return nil, Usagef("at least one manifest format is required")
	}
	if cfg.LateWindow < 0 {
		return nil, Usagef("late window must be non-negative")
	}
	if cfg.QueueBytes <= 0 {
		cfg.QueueBytes = DefaultQueueBytes
	}
	switch cfg.Fault {
	case FaultNone, FaultTruncatedBox, FaultBadReference:
	case FaultLateCheckpoint:
		return runLateCheckpoint(ctx, cfg)
	default:
		return nil, Usagef("unknown fault %q", cfg.Fault)
	}

	runs, err := loadSources(cfg)
	if err != nil {
		return nil, err
	}
	sources, initByLabel, err := demuxInits(cfg, runs)
	if err != nil {
		return nil, err
	}
	tm, err := BuildTrackMap(sources)
	if err != nil {
		return nil, err
	}
	tl, err := NewTimeline(tm, timelineConfigFor(cfg))
	if err != nil {
		return nil, err
	}
	rep := &Report{TrackMapDigest: tm.Hash(), Formats: cfg.Format}
	if err := streamIntoTimeline(ctx, cfg, tl, runs, initByLabel, rep); err != nil {
		return nil, err
	}
	return finishReport(rep, tl, tm)
}

func timelineConfigFor(cfg EngineConfig) TimelineConfig {
	return TimelineConfig{
		LateWindow:  cfg.LateWindow,
		SourceOrder: []string{"main", "backup"},
		WrapModulus: 1 << 32,
		WrapGap:     1 << 31,
	}
}

// loadSources parses every requested manifest format per source and verifies
// that the formats agree on segment timing before choosing one to ingest.
func loadSources(cfg EngineConfig) ([]*sourceRun, error) {
	labels := []string{"main", "backup"}
	runs := make([]*sourceRun, 0, len(labels))
	for _, label := range labels {
		run := &sourceRun{label: label, manifests: map[ManifestKind]*Manifest{}}
		for _, kind := range cfg.Format {
			m, err := LoadSourceManifest(cfg.Root, label, kind)
			if err != nil {
				return nil, err
			}
			run.manifests[kind] = m
		}
		if err := crossCheckFormats(run); err != nil {
			return nil, err
		}
		// Prefer HLS for ingestion when both are present: both must agree, and
		// ingesting twice would duplicate samples.
		if m, ok := run.manifests[ManifestHLS]; ok {
			run.manifest = m
		} else {
			run.manifest = run.manifests[ManifestDASH]
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// crossCheckFormats verifies that HLS and DASH describe the same video segment
// timing for a source, rejecting a fixture set whose manifests disagree.
func crossCheckFormats(run *sourceRun) error {
	hls, okHLS := run.manifests[ManifestHLS]
	dash, okDASH := run.manifests[ManifestDASH]
	if !okHLS || !okDASH {
		return nil
	}
	hlsVideo := representationFor(hls, MediaVideo)
	dashVideo := representationFor(dash, MediaVideo)
	if hlsVideo == nil || dashVideo == nil {
		return nil
	}
	if len(hlsVideo.Segments) != len(dashVideo.Segments) {
		return NewError(KindBadReference, Location{File: run.label, Offset: -1, Sample: -1},
			"HLS has %d video segments but DASH has %d for source %q", len(hlsVideo.Segments), len(dashVideo.Segments), run.label)
	}
	for i := range hlsVideo.Segments {
		h, d := hlsVideo.Segments[i], dashVideo.Segments[i]
		if h.Start != d.Start || h.Duration != d.Duration {
			return NewError(KindBadReference, Location{File: h.URI, Offset: -1, Sample: -1},
				"HLS and DASH disagree at video segment %d: HLS [%d,%d) DASH [%d,%d)",
				i, h.Start, h.Start+h.Duration, d.Start, d.Start+d.Duration)
		}
	}
	return nil
}

// representationFor returns the first representation of the given kind.
func representationFor(m *Manifest, kind MediaKind) *Representation {
	for i := range m.Representations {
		if m.Representations[i].Kind == kind {
			return &m.Representations[i]
		}
	}
	return nil
}

// demuxInits parses every referenced init segment once per source.
func demuxInits(cfg EngineConfig, runs []*sourceRun) ([]TrackSource, map[string]map[string]*InitSegment, error) {
	var sources []TrackSource
	initByLabel := map[string]map[string]*InitSegment{}
	for _, r := range runs {
		initByLabel[r.label] = map[string]*InitSegment{}
		for _, rep := range r.manifest.Representations {
			if rep.InitURI == "" {
				return nil, nil, NewError(KindParse, Location{File: r.manifest.Path, Offset: -1, Sample: -1},
					"representation %q has no initialization segment", rep.ID)
			}
			if _, ok := initByLabel[r.label][rep.InitURI]; ok {
				continue
			}
			data, err := readFixture(cfg.Root, rep.InitURI)
			if err != nil {
				return nil, nil, err
			}
			init, err := ParseInit(rep.InitURI, data)
			if err != nil {
				return nil, nil, err
			}
			initByLabel[r.label][rep.InitURI] = init
		}
		sources = append(sources, TrackSource{Label: r.label, Init: mergedInit(initByLabel[r.label])})
	}
	return sources, initByLabel, nil
}

// mergedInit unions the tracks of every init segment referenced by one source.
func mergedInit(m map[string]*InitSegment) *InitSegment {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	merged := &InitSegment{}
	seen := map[uint32]bool{}
	for _, k := range keys {
		for _, tr := range m[k].Tracks {
			if seen[tr.SourceID] {
				continue
			}
			seen[tr.SourceID] = true
			merged.Tracks = append(merged.Tracks, tr)
		}
	}
	return merged
}

// arrival is one sample with the source label it came from.
type arrival struct {
	source string
	sample Sample
}

// streamIntoTimeline demuxes all media through a bounded queue, then feeds the
// timeline in a deterministic order: main fully first, then backup. This models
// the switchover where the backup's overlapping segments arrive afterwards.
func streamIntoTimeline(ctx context.Context, cfg EngineConfig, tl *Timeline, runs []*sourceRun, initByLabel map[string]map[string]*InitSegment, rep *Report) error {
	q, err := NewQueue(ctx, QueueConfig{MaxBytes: cfg.QueueBytes})
	if err != nil {
		return err
	}
	defer q.Close()

	// Produce in a goroutine so the bounded queue actually applies pressure.
	produced := make(chan error, 1)
	go func() {
		defer q.Close()
		for _, r := range runs {
			for _, ref := range r.manifest.Segments() {
				data, err := readSegment(cfg.Root, ref)
				if err != nil {
					produced <- err
					return
				}
				init := initByLabel[r.label][ref.InitURI]
				if init == nil {
					produced <- NewError(KindParse, Location{File: ref.URI, Offset: -1, Sample: -1}, "missing init %q", ref.InitURI)
					return
				}
				seg, err := ParseSegment(init, ref.URI, data)
				if err != nil {
					produced <- err
					return
				}
				for _, s := range seg.Samples {
					if err := q.Push(s); err != nil {
						produced <- wrapQueue(err, s)
						return
					}
				}
			}
		}
		produced <- nil
	}()

	var arrivals []arrival
	for {
		s, err := q.Pop()
		if err != nil {
			break
		}
		arrivals = append(arrivals, arrival{source: sourceOf(s, runs), sample: s})
	}
	if err := <-produced; err != nil {
		return err
	}
	sortArrivals(arrivals, cfg)
	for _, a := range arrivals {
		if err := tl.Add(a.source, a.sample); err != nil {
			if IsKind(err, KindLate) {
				rep.LateRejected = tl.stats.LateRejected
				continue
			}
			return err
		}
	}
	rep.LateRejected = tl.stats.LateRejected
	return nil
}

// sortArrivals orders samples deterministically: by source priority
// (main before backup), then segment, then decode time and index. Main-first is
// the switchover model: the backup's overlapping and stale segments arrive
// after the primary has already advanced the watermark.
func sortArrivals(in []arrival, cfg EngineConfig) {
	rank := map[string]int{}
	for i, name := range timelineConfigFor(cfg).SourceOrder {
		rank[name] = i
	}
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if a.source != b.source {
			return rank[a.source] < rank[b.source]
		}
		if a.sample.Segment != b.sample.Segment {
			return a.sample.Segment < b.sample.Segment
		}
		if a.sample.DTS != b.sample.DTS {
			return a.sample.DTS < b.sample.DTS
		}
		return a.sample.Index < b.sample.Index
	})
}

func sourceOf(s Sample, runs []*sourceRun) string {
	for _, r := range runs {
		if strings.HasPrefix(s.Segment, r.label+"/") {
			return r.label
		}
	}
	return runs[0].label
}

func wrapQueue(err error, s Sample) error {
	if IsKind(err, KindQueueLimit) {
		return err
	}
	return WrapError(err, KindQueueLimit, Location{File: s.File, Segment: s.Segment, Offset: s.ByteOffset, Sample: s.Index},
		"stream cancelled while enqueuing sample")
}

func readFixture(root, rel string) ([]byte, error) {
	if err := validateRelativeRef(rel); err != nil {
		return nil, NewError(KindPathEscape, locWithFile(rel, 0), "fixture reference rejected: %v", err)
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil {
		return nil, WrapError(err, KindBadReference, locWithFile(rel, 0), "fixture not readable")
	}
	if info.Size() > maxSegmentBytes {
		return nil, NewError(KindParse, locWithFile(rel, 0), "fixture is %d bytes, limit %d", info.Size(), maxSegmentBytes)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, WrapError(err, KindBadReference, locWithFile(rel, 0), "fixture not readable")
	}
	return data, nil
}

func readSegment(root string, ref SegmentRef) ([]byte, error) {
	data, err := readFixture(root, ref.URI)
	if err != nil {
		return nil, err
	}
	if ref.ByteRange != nil {
		if ref.ByteRange.Start < 0 || ref.ByteRange.End > int64(len(data)) || ref.ByteRange.Start >= ref.ByteRange.End {
			return nil, NewError(KindBadReference, Location{File: ref.URI, Offset: ref.ByteRange.Start, Sample: -1},
				"byte range [%d,%d) exceeds the %d-byte segment", ref.ByteRange.Start, ref.ByteRange.End, len(data))
		}
		return data[ref.ByteRange.Start:ref.ByteRange.End], nil
	}
	return data, nil
}

// finishReport recomputes, remuxes, round-trips the output and fills the report.
func finishReport(rep *Report, tl *Timeline, tm *TrackMap) (*Report, error) {
	prog, err := tl.Recompute()
	if err != nil {
		return nil, err
	}
	out, err := MuxProgram(prog, OutputSegmentTicks)
	if err != nil {
		return nil, err
	}
	timeDelta, byteDelta, segments, totalBytes, err := verifyRoundTrip(prog, out)
	if err != nil {
		return nil, err
	}
	rep.Samples = prog.Stats.Samples
	rep.LateRecomputed = prog.Stats.LateRecomputed
	rep.LateRejected = prog.Stats.LateRejected
	rep.TimeDeltaTicks = timeDelta
	rep.ByteDelta = byteDelta
	rep.MappingMissing = len(tm.MissingTargets())
	rep.Gaps = prog.Stats.Gaps
	rep.Duplicates = prog.Stats.Duplicates
	rep.OverlapsDropped = prog.Stats.OverlapsDropped
	rep.Segments = segments
	rep.Bytes = totalBytes
	rep.SHA256 = prog.Digest(out.DASH, out.DigestInput())
	rep.Lines = describeSamples(prog, out)
	rep.Tracks = prog.Tracks
	rep.MappingLines = tm.Describe()
	rep.FailureLocation = prog.Stats.FailureLocation
	rep.ensure()
	return rep, nil
}

// verifyRoundTrip re-demuxes the remuxed output and measures the timing and byte
// deviation from the program. Any decompression or loss surfaces as a non-zero
// delta and fails the run.
func verifyRoundTrip(prog *Program, out *MuxOutput) (timeDelta, byteDelta int64, segments int, totalBytes int64, err error) {
	for key, fragments := range out.Segments {
		init, ierr := ParseInit(key+"/init.mp4", out.Init[key])
		if ierr != nil {
			return 0, 0, 0, 0, WrapError(ierr, KindMux, Location{Offset: -1, Sample: -1}, "remuxed init for %s is invalid", key)
		}
		var remuxed []Sample
		for i, frag := range fragments {
			segments++
			totalBytes += int64(len(frag))
			parsed, perr := ParseSegment(init, SegmentName(key, i), frag)
			if perr != nil {
				return 0, 0, 0, 0, WrapError(perr, KindMux, Location{File: SegmentName(key, i), Offset: -1, Sample: -1},
					"remuxed fragment for %s does not demux", key)
			}
			remuxed = append(remuxed, parsed.Samples...)
		}
		// Compare in decode order: the remuxer writes fragme	nts in DTS order.
		var want []PlacedSample
		for _, s := range prog.Samples {
			if s.Target == key {
				want = append(want, s)
			}
		}
		sort.SliceStable(want, func(i, j int) bool { return want[i].DTS < want[j].DTS })
		if len(remuxed) != len(want) {
			return 0, 0, 0, 0, NewError(KindMux, Location{Offset: -1, Sample: -1},
				"track %s remuxed %d samples but the program has %d", key, len(remuxed), len(want))
		}
		for i := range want {
			d := remuxed[i].PTS - want[i].PTS
			if d < 0 {
				d = -d
			}
			timeDelta += int64(d)
			byteDelta += byteDeviation(remuxed[i].Data, want[i].Data)
		}
	}
	return timeDelta, byteDelta, segments, totalBytes, nil
}

// byteDeviation counts bytes that differ between source and remuxed payloads.
func byteDeviation(got, want []byte) int64 {
	if len(got) == len(want) {
		var d int64
		for i := range got {
			if got[i] != want[i] {
				d++
			}
		}
		return d
	}
	d := int64(len(got) - len(want))
	if d < 0 {
		d = -d
	}
	common := len(got)
	if len(want) < common {
		common = len(want)
	}
	for i := 0; i < common; i++ {
		if got[i] != want[i] {
			d++
		}
	}
	return d
}

// describeSamples renders per-sample provenance, target mapping, start/end
// times and both source and output byte ranges.
func describeSamples(prog *Program, out *MuxOutput) []string {
	lines := make([]string, 0, len(prog.Samples))
	for _, s := range prog.Samples {
		outRange, ok := out.OutBytes[OutputSampleKey(s.Target, s)]
		outText := "none"
		if ok {
			outText = fmt.Sprintf("[%d,%d)", outRange.Start, outRange.End)
		}
		lines = append(lines, fmt.Sprintf(
			"sample source=%s track=%d target=%s dts=%d pts=%d start=%d end=%d sync=%t src_bytes=[%d,%d) out_bytes=%s time_delta=%d",
			s.Source, s.SourceID, s.Target, s.DTS, s.PTS, s.PTS, s.End(), s.Sync,
			s.ByteOffset, s.ByteOffset+s.ByteLength, outText, s.PTS-s.SourcePTS))
	}
	return lines
}
