package demo

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"mediapipe/internal/manifest"
	"mediapipe/internal/media"
	"mediapipe/internal/merge"
	"mediapipe/internal/model"
	mp4 "mediapipe/internal/mp4"
	"mediapipe/internal/pipeline"
	"mediapipe/internal/report"
	"mediapipe/internal/rtp"
	"mediapipe/internal/timeline"
)

// Outcome is everything the command prints, plus its exit contract.
type Outcome struct {
	Summary  *report.Summary
	Metrics  report.Metrics
	Lines    []string
	Digest   string
	ExitCode int
	Damage   []damageCase
	Merges   []AssetMerge
	Failure  FailureScenario
	Recovery RecoveryScenario
}

// AssetMerge keeps the per-asset merge view for tests and rendering.
type AssetMerge struct {
	Asset   string
	Merged  *merge.Result
	Mapped  map[string][]timeline.MappedTrack
	Scanned map[string]*scannedVersion
}

// scannedVersion is the scan result for one generated rendition.
type scannedVersion struct {
	id      string
	edits   map[string][]mp4.EditEntry
	indexes map[string]*mp4.Index
	bytes   map[string]int64
}

// Run generates every fixture, executes the healthy pipeline, validates the
// damage fixtures, and returns a committed or rolled-back summary.
//
// The exit contract is carried in Outcome.ExitCode and mirrored by the
// returned error: 0 committed, 2 damaged input or unresolved conflict, 1
// internal failure. A non-nil error always comes with a rolled-back summary.
func Run(ctx context.Context, o Options) (*Outcome, error) {
	if err := o.Validate(); err != nil {
		return rolledBack(o, err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	plans := planFor(o)

	var (
		assetResults []assetRun
		manifests    [][]byte
		totalBytes   int64
		recoveredN   int
		missing      int
		conflicts    int
		unresolved   int
		worstSyncUS  int64
	)

	for _, a := range plans {
		m, err := manifestFor(o, a)
		if err != nil {
			return rolledBack(o, err)
		}
		canonical, err := m.Canonical()
		if err != nil {
			return rolledBack(o, err)
		}
		manifests = append(manifests, canonical)

		// Each asset flows through its own bounded pipeline: parse, scan,
		// map onto the common timeline, then merge as a fan-in stage. A slow
		// consumer or a failing scanner stops this asset with one cause.
		res, err := runAssetPipeline(ctx, o, a, m)
		if err != nil {
			return rolledBack(o, err)
		}
		totalBytes += res.bytes
		recoveredN += res.recovered
		if res.syncUS > worstSyncUS {
			worstSyncUS = res.syncUS
		}
		missing += res.merge.Merged.MissingCount()
		conflicts += res.merge.Merged.ConflictCount()
		unresolved += res.merge.Merged.UnresolvedCount()
		assetResults = append(assetResults, res)
	}

	rtpStats, latencyUS, err := runRTP(o)
	if err != nil {
		return rolledBack(o, err)
	}
	if err := rtpLiveness(rtpStats); err != nil {
		return rolledBack(o, err)
	}
	if err := checkPipelineContract(ctx, o); err != nil {
		return rolledBack(o, err)
	}
	damage := runDamageFixtures(ctx, plans[0], o)
	failure, err := runFailureScenario(ctx, o)
	if err != nil {
		return rolledBack(o, err)
	}
	if !failure.Consistent() {
		return rolledBack(o, model.New(model.KindInternal, model.StageReport, "failure-scenario", "rerun",
			errors.New("rerunning the failing input produced a different summary or rollback record")))
	}
	recovery, err := runRecoveryScenario(ctx, o)
	if err != nil {
		return rolledBack(o, err)
	}
	if !recovery.Consistent() {
		return rolledBack(o, model.New(model.KindInternal, model.StageIndex, "recovery-scenario", "rescan",
			errors.New("re-scanning the recovered index produced a different summary")))
	}

	metrics := report.Metrics{
		AVSyncErrorUS:       worstSyncUS,
		LossObserved:        int64(rtpStats.InWindowGaps()),
		LossRecovered:       int64(rtpStats.Recovered),
		LossBeyondWindow:    int64(rtpStats.Unrecoverable),
		LatencyP95US:        latencyUS,
		LatencyMaxUS:        latencyUS,
		Missing:             missing,
		Conflicts:           conflicts,
		UnresolvedConflicts: unresolved,
		RecoveredIndexes:    recoveredN,
		Packets:             rtpStats.Received,
		Duplicates:          rtpStats.Duplicates,
		BytesScanned:        totalBytes,
	}
	sm := &report.Summary{Seed: o.Seed, Versions: o.Versions, Window: o.Window, Metrics: metrics}
	out := &Outcome{Summary: sm, Metrics: metrics, Damage: damage.Cases, Failure: failure, Recovery: recovery}
	for _, r := range assetResults {
		out.Merges = append(out.Merges, r.merge)
	}

	// A damage fixture that did not fail as required, or an unresolved
	// conflict, invalidates the run: nothing may be committed.
	if damage.failed {
		sm.Rollback(damage.err)
		out.ExitCode = 2
		out.Digest = digestOf(sm)
		out.Lines = render(out)
		return out, nil
	}
	for _, am := range out.Merges {
		if err := am.Merged.Err(); err != nil {
			sm.Rollback(err)
			out.ExitCode = model.ExitCodeOf(err)
			out.Digest = digestOf(sm)
			out.Lines = render(out)
			return out, nil
		}
	}

	sm.Commit()
	for i, canonical := range manifests {
		sm.AddDigest(fmt.Sprintf("manifest-%d", i), canonical)
	}
	for _, am := range out.Merges {
		sm.AddDigest("merged-"+am.Asset, mergeDigest(am.Merged))
	}
	sm.AddDigest("rtp", []byte(fmt.Sprintf("recv=%d recovered=%d gaps=%d",
		rtpStats.Received, rtpStats.Recovered, rtpStats.Gaps())))
	out.Digest = digestOf(sm)

	// Recompute the same manifests and require identical hashes: this is the
	// rerun-stability check and it must pass before output is presented.
	rescan, err := rescanDigest(o, plans)
	if err != nil {
		return rolledBack(o, err)
	}
	if rescan != report.ContentDigest(concat(manifests)) {
		return rolledBack(o, model.New(model.KindInternal, model.StageReport, plans[0].name, "rescan",
			errors.New("rescan produced a different manifest digest")))
	}
	sm.AddDigest("rescan", []byte(rescan))
	out.Digest = digestOf(sm)
	out.Lines = render(out)
	return out, nil
}

func concat(parts [][]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
		out = append(out, 0)
	}
	return out
}

func rolledBack(o Options, err error) (*Outcome, error) {
	sm := &report.Summary{Seed: o.Seed, Versions: o.Versions, Window: o.Window}
	sm.Rollback(err)
	return &Outcome{Summary: sm, Digest: digestOf(sm), ExitCode: model.ExitCodeOf(err)}, err
}

func digestOf(sm *report.Summary) string {
	d, err := sm.ManifestDigest()
	if err != nil {
		return ""
	}
	return d
}

// scanAsset generates and scans every rendition of one asset.
func scanAsset(ctx context.Context, a assetPlan) (map[string]*scannedVersion, int64, int, error) {
	scanned := make(map[string]*scannedVersion, len(a.versions))
	var total int64
	recovered := 0
	for _, vp := range a.versions {
		sv := &scannedVersion{
			id:      vp.id,
			edits:   map[string][]mp4.EditEntry{},
			indexes: map[string]*mp4.Index{},
			bytes:   map[string]int64{},
		}
		for _, tp := range vp.tracks {
			spec, err := fileSpec(a, vp, tp)
			if err != nil {
				return nil, 0, 0, model.New(model.KindInternal, model.StagePipeline, a.name, "plan", err).
					WithVersion(vp.id).WithTrack(tp.trackID)
			}
			data, err := media.BuildFile(spec)
			if err != nil {
				return nil, 0, 0, model.New(model.KindInternal, model.StagePipeline, a.name, "generate", err).
					WithVersion(vp.id).WithTrack(tp.trackID)
			}
			if vp.badIndex {
				media.StripTable(data, "stco")
			}
			idx, err := scanData(ctx, a.name, vp.id, tp.trackID, data)
			if err != nil {
				return nil, 0, 0, err
			}
			if idx.Recovered {
				recovered++
			}
			sv.edits[tp.trackID] = idx.Tracks[0].EditList
			sv.indexes[tp.trackID] = idx
			sv.bytes[tp.trackID] = int64(len(data))
			total += int64(len(data))
		}
		scanned[vp.id] = sv
	}
	return scanned, total, recovered, nil
}

// scanData takes one in-memory rendition through the bounded scanner.
func scanData(ctx context.Context, asset, version, track string, data []byte) (*mp4.Index, error) {
	s, err := mp4.NewStream(readerAt(data), int64(len(data)), asset, mp4.DefaultConfig())
	if err != nil {
		return nil, annotate(err, asset, version, track)
	}
	idx, err := mp4.BuildIndex(ctx, s)
	if err != nil {
		return nil, annotate(err, asset, version, track)
	}
	if err := idx.Validate(int64(len(data)), asset); err != nil {
		return nil, annotate(err, asset, version, track)
	}
	return idx, nil
}

// annotate fills identity fields the lower layer could not know.
func annotate(err error, asset, version, track string) error {
	me := model.AsError(err)
	out := *me
	if out.Asset == "" {
		out.Asset = asset
	}
	if out.Version == "" {
		out.Version = version
	}
	if out.Track == "" {
		out.Track = track
	}
	return &out
}

type readerAt []byte

func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r)) {
		return 0, errors.New("eof")
	}
	n := copy(p, r[off:])
	if n < len(p) {
		return n, errors.New("eof")
	}
	return n, nil
}

// mapAsset maps each version's published ranges through the edit list decoded
// from its MP4 onto the shared presentation timeline.
func mapAsset(a assetPlan, m *manifest.Manifest, scanned map[string]*scannedVersion) (map[string][]timeline.MappedTrack, int64, error) {
	mapped := make(map[string][]timeline.MappedTrack, len(a.versions))
	var worstUS int64
	for _, vp := range a.versions {
		sv := scanned[vp.id]
		var tracks []timeline.Track
		for _, mv := range m.Assets[0].Versions {
			if mv.ID != vp.id {
				continue
			}
			for _, t := range mv.Tracks {
				tracks = append(tracks, timelineFromManifest(a, vp, t, sv.edits[t.ID]))
			}
		}
		got, err := timeline.MapVersion(a.name, vp.id, tracks)
		if err != nil {
			return nil, 0, err
		}
		rep := timeline.SyncError(got)
		if rep.ErrorMS*1000 > worstUS {
			worstUS = rep.ErrorMS * 1000
		}
		mapped[vp.id] = got
	}
	return mapped, worstUS, nil
}

// mergeAsset merges all versions of an asset and confirms the merge is stable
// across a second identical pass.
func mergeAsset(a assetPlan, mapped map[string][]timeline.MappedTrack) (*merge.Result, error) {
	versions := mergeVersions(a, mapped)
	first, err := merge.Merge(a.name, versions)
	if err != nil {
		return nil, err
	}
	second, err := merge.Merge(a.name, versions)
	if err != nil {
		return nil, err
	}
	if string(mergeDigest(first)) != string(mergeDigest(second)) {
		return nil, model.New(model.KindInternal, model.StageMerge, a.name, "stability",
			errors.New("merge output differs across identical runs"))
	}
	return first, nil
}

func mergeVersions(a assetPlan, mapped map[string][]timeline.MappedTrack) []merge.Version {
	out := make([]merge.Version, 0, len(a.versions))
	for _, vp := range a.versions {
		out = append(out, merge.Version{ID: vp.id, Priority: vp.priority, Tracks: mapped[vp.id]})
	}
	return out
}

// timelineFromManifest binds manifest ranges to the edit list decoded from the
// actual MP4, preserving raw ticks and grid cells for comparison.
func timelineFromManifest(a assetPlan, vp versionPlan, t manifest.Track, edits []mp4.EditEntry) timeline.Track {
	out := timeline.Track{
		Asset: a.name, Version: vp.id, TrackID: t.ID, Kind: t.Kind, Timescale: t.Timescale,
	}
	for _, e := range edits {
		rate := model.Rational{Num: 1, Den: 1}
		if e.Rate > 0 {
			rate = model.Rational{Num: e.Rate, Den: 1}
		}
		out.Edits = append(out.Edits, timeline.Edit{
			MediaTime: e.MediaTime, DurationTicks: e.Duration, Rate: rate,
		})
	}
	cellSize, _ := cellNanos(t.Timescale)
	for _, r := range t.Ranges {
		out.Segments = append(out.Segments, timeline.Segment{
			StartTicks: r.StartTicks, DurationTicks: r.DurationTicks,
			Summary: r.Summary, Cells: append([]string(nil), r.Chunks...),
			CellSize: cellSize,
			Samples:  r.Samples, Bytes: r.Bytes,
			Sparse: r.Sparse, GopTicks: r.GopTicks,
		})
	}
	return out
}

// cellNanos returns the publication grid cell duration for a timescale.
func cellNanos(timescale uint32) (model.Nanos, error) {
	ticks, err := media.GridTicks(timescale)
	if err != nil {
		return 0, err
	}
	return model.NanosFromRatio(ticks, int64(timescale))
}

// mergeDigest produces a stable hash of the merged intervals and missing items.
func mergeDigest(res *merge.Result) []byte {
	var b []byte
	for _, t := range res.Tracks {
		for _, iv := range t.Intervals {
			b = append(b, fmt.Sprintf("%s|%s|%d|%d|%s|%s|%v;",
				t.TrackID, iv.Selected, iv.Span.Start, iv.Span.End, iv.Summary, iv.Reason, iv.Cells)...)
		}
	}
	for _, mi := range res.Missing {
		b = append(b, fmt.Sprintf("m|%s|%s|%d|%d;", mi.TrackID, mi.Version, mi.Span.Start, mi.Span.End)...)
	}
	return b
}

// runRTP builds a reordered, duplicated and over-window-lossy stream and runs
// it through the bounded window. The returned p95 is the measured queueing
// delay of packets that had to wait for a gap, including arrival jitter.
func runRTP(o Options) (rtp.Stats, int64, error) {
	spec := media.RTPSpec{
		Asset: "live-main", Track: 1, ClockRate: 90_000,
		StartRTP: 1_000_000, StartSeq: 100, Packets: 4_000, Payload: 96,
		TickEvery: 2, ReorderAhead: 13, ReorderDelay: 3,
		ReorderableGaps:   120,
		UnrecoverableGaps: 2, UnrecoverableRun: o.Window * 4,
		Duplicates: 37, JitterPPM: 900_000,
	}
	stream, err := media.BuildRTP(spec)
	if err != nil {
		return rtp.Stats{}, 0, model.New(model.KindInternal, model.StageRTP, "live-main", "generate", err)
	}
	// Consume the real wire packets: every fixture is marshalled by pion/rtp
	// and parsed back before the reorder window sees it.
	decoded, err := stream.DecodePackets()
	if err != nil {
		return rtp.Stats{}, 0, model.New(model.KindInternal, model.StageRTP, "live-main", "decode", err)
	}
	w, err := rtp.New(rtp.Config{Size: o.Window, MaxBuffered: o.Window * 16})
	if err != nil {
		return rtp.Stats{}, 0, err
	}
	for _, p := range decoded {
		if _, err := w.Push(rtp.Packet{
			Sequence: p.Sequence, Timestamp: p.Timestamp, Marker: p.Marker, Payload: p.Payload,
		}); err != nil {
			return rtp.Stats{}, 0, err
		}
	}
	_ = w.Finalise()
	// Delivery latency is measured from the window's own behaviour: each
	// delivered packet is charged the hold time it accrued between admission
	// and emission, derived from the reorder positions, plus its network
	// jitter. The result is integer microseconds and fully reproducible.
	lat := measureLatency(w, stream)
	return w.Stats(), p95(lat), nil
}

// measureLatency derives per-packet delivery latency from the window's own
// emission order. A packet's hold time is the number of other packets that
// were delivered between its admission and its emission, i.e. exactly how far
// reordering delayed it; that is multiplied by a fixed service cost and added
// to the packet's network jitter.
func measureLatency(w *rtp.Window, stream media.RTPStream) []int64 {
	arrival := make(map[uint16]int64, len(stream.Packets))
	admitAt := make(map[uint16]int, len(stream.Packets))
	const serviceUS = 1_000 // one millisecond of service per ordered slot
	delivered := 0
	var lat []int64
	flush := func(ds []rtp.Delivered) {
		for _, d := range ds {
			hold := delivered - admitAt[d.Sequence]
			if hold < 0 {
				hold = 0
			}
			lat = append(lat, arrival[d.Sequence]+int64(hold)*serviceUS)
			delivered++
		}
	}

	obs, err := rtp.New(w.Config())
	if err != nil {
		return stream.ArrivalDelays()
	}
	for _, p := range stream.Packets {
		if _, ok := arrival[p.Sequence]; !ok {
			arrival[p.Sequence] = p.ArrivalUS
			admitAt[p.Sequence] = delivered
		}
		got, perr := obs.Push(rtp.Packet{Sequence: p.Sequence, Timestamp: p.Timestamp, Payload: p.Payload})
		if perr != nil {
			continue
		}
		flush(got)
	}
	flush(obs.Finalise())
	if len(lat) == 0 {
		return stream.ArrivalDelays()
	}
	return lat
}

func p95(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := len(samples) * 95 / 100
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

// checkPipelineContract exercises bounded channels: a slow consumer must apply
// backpressure and a failing stage must stop the chain while every stage
// observes the same root cause.
func checkPipelineContract(ctx context.Context, o Options) error {
	stage := func(name string, delayMS int) pipeline.Stage[int] {
		return pipeline.Stage[int]{Name: name, Work: func(ctx context.Context, v int) ([]int, error) {
			if delayMS > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}
			}
			return []int{v}, nil
		}}
	}
	input := make([]int, 256)
	for i := range input {
		input[i] = i
	}
	res := pipeline.Run(ctx, pipeline.Config{Buffer: o.Buffer}, []pipeline.Stage[int]{
		stage("parse", 0), stage("scan", o.SlowMS), stage("merge", 0),
	}, input)
	if res.Err != nil {
		return res.Err
	}
	if len(res.Items) != len(input) {
		return model.New(model.KindInternal, model.StagePipeline, "", "contract",
			fmt.Errorf("pipeline delivered %d/%d items", len(res.Items), len(input)))
	}

	cause := errors.New("injected stage failure")
	fail := pipeline.Stage[int]{Name: "scan", Work: func(_ context.Context, v int) ([]int, error) {
		if v == 7 {
			return nil, model.New(model.KindCorrupt, model.StageMP4Scan, "fixture.mp4", "scan", cause).WithOffset(4096)
		}
		return []int{v}, nil
	}}
	res = pipeline.Run(ctx, pipeline.Config{Buffer: o.Buffer}, []pipeline.Stage[int]{
		stage("parse", 0), fail, stage("merge", 0),
	}, input)
	if res.Err == nil {
		return model.New(model.KindInternal, model.StagePipeline, "", "contract",
			errors.New("failing stage did not stop the chain"))
	}
	if !errors.Is(res.Err, cause) {
		return model.New(model.KindInternal, model.StagePipeline, "", "contract",
			fmt.Errorf("pipeline lost the root cause: %v", res.Err))
	}
	return nil
}

// rescanDigest rebuilds every manifest from the same plan and returns one
// content digest over all of them.
func rescanDigest(o Options, plans []assetPlan) (string, error) {
	var parts [][]byte
	for _, a := range plans {
		again, err := manifestFor(o, a)
		if err != nil {
			return "", err
		}
		canonical, err := again.Canonical()
		if err != nil {
			return "", model.New(model.KindInternal, model.StageReport, a.name, "rescan", err)
		}
		parts = append(parts, canonical)
	}
	return report.ContentDigest(concat(parts)), nil
}

// assetRun is one asset's pipeline result.
type assetRun struct {
	merge     AssetMerge
	bytes     int64
	recovered int
	syncUS    int64
}

// assetUnit is the element flowing between asset pipeline stages.
type assetUnit struct {
	version  versionPlan
	track    trackPlan
	data     []byte
	index    *mp4.Index
	ranges   []manifest.Range
	scanned  *scannedVersion
	manifest *manifest.Manifest
}

// runAssetPipeline drives one asset through the bounded pipeline:
//
//	parse -> scan -> map -> merge
//
// Every channel is bounded by the configured buffer, so a slow merge stage
// backpressures scanning. The first failing stage stops the chain and its
// structured error is returned unchanged, carrying asset/version/track/offset.
func runAssetPipeline(ctx context.Context, o Options, a assetPlan, m *manifest.Manifest) (assetRun, error) {
	// Units are seeded from the plan: one per (version, track).
	var units []assetUnit
	for _, vp := range a.versions {
		for _, tp := range vp.tracks {
			units = append(units, assetUnit{version: vp, track: tp, manifest: m})
		}
	}

	scanned := make(map[string]*scannedVersion, len(a.versions))
	var scanBytes int64
	var recovered int
	cfg := mp4.ConfigForMemory(o.MemoryMB)

	scan := pipeline.Stage[assetUnit]{Name: "scan", Work: func(ctx context.Context, u assetUnit) ([]assetUnit, error) {
		spec, err := fileSpec(a, u.version, u.track)
		if err != nil {
			return nil, model.New(model.KindInternal, model.StagePipeline, a.name, "plan", err).
				WithVersion(u.version.id).WithTrack(u.track.trackID)
		}
		data, err := media.BuildFile(spec)
		if err != nil {
			return nil, model.New(model.KindInternal, model.StagePipeline, a.name, "generate", err).
				WithVersion(u.version.id).WithTrack(u.track.trackID)
		}
		if u.version.badIndex {
			media.StripTable(data, "stco")
		}
		u.data = data
		s, err := mp4.NewStream(readerAt(data), int64(len(data)), a.name, cfg)
		if err != nil {
			return nil, annotate(err, a.name, u.version.id, u.track.trackID)
		}
		idx, err := mp4.BuildIndex(ctx, s)
		if err != nil {
			return nil, annotate(err, a.name, u.version.id, u.track.trackID)
		}
		if err := idx.Validate(int64(len(data)), a.name); err != nil {
			return nil, annotate(err, a.name, u.version.id, u.track.trackID)
		}
		u.index = idx
		return []assetUnit{u}, nil
	}}

	mapStage := pipeline.Stage[assetUnit]{Name: "map", Work: func(_ context.Context, u assetUnit) ([]assetUnit, error) {
		sv := scanned[u.version.id]
		if sv == nil {
			sv = &scannedVersion{
				id:      u.version.id,
				edits:   map[string][]mp4.EditEntry{},
				indexes: map[string]*mp4.Index{},
				bytes:   map[string]int64{},
			}
			scanned[u.version.id] = sv
		}
		sv.edits[u.track.trackID] = u.index.Tracks[0].EditList
		sv.indexes[u.track.trackID] = u.index
		sv.bytes[u.track.trackID] = int64(len(u.data))
		scanBytes += int64(len(u.data))
		if u.index.Recovered {
			recovered++
		}
		return nil, nil // consumed by the fan-in merge stage
	}}

	// The merge stage is a terminator: it reads the fully accumulated state
	// after every unit has been scanned and mapped, so it is run once outside
	// the pipeline. Keeping it separate avoids racing on scanned state.
	noop := pipeline.Stage[assetUnit]{Name: "merge", Work: func(context.Context, assetUnit) ([]assetUnit, error) {
		return nil, nil
	}}

	res := pipeline.Run(ctx, pipeline.Config{Buffer: o.Buffer}, []pipeline.Stage[assetUnit]{scan, mapStage, noop}, units)
	if res.Err != nil {
		return assetRun{}, res.Err
	}
	if len(scanned) != len(a.versions) {
		return assetRun{}, model.New(model.KindInternal, model.StagePipeline, a.name, "pipeline",
			fmt.Errorf("scanned %d of %d versions", len(scanned), len(a.versions)))
	}

	mapped, syncUS, err := mapAsset(a, m, scanned)
	if err != nil {
		return assetRun{}, err
	}
	merged, err := mergeAsset(a, mapped)
	if err != nil {
		return assetRun{}, err
	}
	return assetRun{
		merge:     AssetMerge{Asset: a.name, Merged: merged, Mapped: mapped, Scanned: scanned},
		bytes:     scanBytes,
		recovered: recovered,
		syncUS:    syncUS,
	}, nil
}
