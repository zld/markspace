package demo

import (
	"fmt"

	"mediapipe/internal/manifest"
	"mediapipe/internal/media"
	"mediapipe/internal/model"
)

// Options are the CLI-visible knobs. Defaults come from cmd/regress.
type Options struct {
	Seed     int64
	Duration int // seconds of generated media
	Versions int
	Window   int
	Buffer   int
	MemoryMB int
	SlowMS   int // per-item delay of the deliberately slow consumer stage
}

// Validate reports whether the requested run is meaningful for the fixtures.
func (o Options) Validate() error {
	fail := func(format string, args ...any) error {
		return model.New(model.KindInvalid, model.StagePipeline, "", "flags", fmt.Errorf(format, args...))
	}
	switch {
	case o.Versions < 2:
		return fail("versions must be >= 2, got %d", o.Versions)
	case o.Duration < 1:
		return fail("duration must be >= 1s, got %d", o.Duration)
	case o.Duration > 3600:
		return fail("duration %ds exceeds the one-hour fixture limit", o.Duration)
	case o.Window < 8:
		return fail("window must be >= 8, got %d", o.Window)
	case o.Buffer < 1:
		return fail("buffer must be >= 1, got %d", o.Buffer)
	case o.MemoryMB < 1:
		return fail("memory limit must be >= 1 MiB, got %d", o.MemoryMB)
	}
	return nil
}

// trackPlan is one track of one generated version. All offsets that describe
// media time are held in nanoseconds and converted to the track timescale, so
// video and audio stay aligned despite different tick rates.
type trackPlan struct {
	kind      string
	trackID   string
	id        uint32
	handler   string
	timescale uint32
	delta     int64 // sample duration in ticks (always a whole grid cell or divisor)
	count     int
	size      uint32
	keyEvery  int
	cellTicks int64 // summary grid cell in ticks
}

// samplesPerCell returns how many samples fit one grid cell.
func (t trackPlan) samplesPerCell() int64 { return t.cellTicks / t.delta }

// versionPlan is one generated rendition.
type versionPlan struct {
	id       string
	priority *int
	tracks   []trackPlan
	// startNS is the raw media start; the edit list hides it from the
	// presentation timeline, as a repackaged recording would.
	startNS int64
	// emptyEditNS delays the whole track without consuming media.
	emptyEditNS int64
	// dropTailNS removes the end of every track so the merge has genuinely
	// missing coverage to itemise.
	dropTailNS int64
	// audioDelayNS delays only the audio track's presentation via its empty
	// edit, creating a real, measurable A/V skew. It is a presentation
	// offset, so it need not align to the media summary grid.
	audioDelayNS int64
	badIndex     bool
	// corruptSummaryAt is a grid cell index whose content digest is altered so
	// the merge sees a genuine summary conflict on exactly that cell. It is
	// -1 (noCorruptCell) when the version keeps its content intact, because
	// cell 0 is a valid target and must not collide with a zero value.
	corruptSummaryAt int64
	// sliceCells forces every published range to span this many grid cells.
	// Zero selects the per-version default derived from its position.
	sliceCells int64
}

// assetPlan groups the versions of one generated asset.
type assetPlan struct {
	name     string
	versions []versionPlan
	// trackKind selects the track layout: "av" or "av-probe" (same layout,
	// only used for isolated conflict checks).
	layout string
}

func intPtr(v int) *int { return &v }

// noCorruptCell marks a rendition whose content digests are untouched.
const noCorruptCell int64 = -1

// planFor generates the fixture plan: one primary multi-version asset plus a
// small conflict-probe asset whose disagreement is resolved by explicit
// priority.
func planFor(o Options) []assetPlan {
	main := assetPlan{name: "live-main"}
	for v := 0; v < o.Versions; v++ {
		vp := versionPlan{id: fmt.Sprintf("v%d", v+1), corruptSummaryAt: noCorruptCell}
		if v == 0 {
			vp.priority = intPtr(10)
		}
		vp.tracks = append(vp.tracks, avTracks(o)...)
		// Each rendition starts its raw media at a different point and hides
		// that with the edit list. The presentation origin therefore stays
		// common while raw timestamps differ.
		vp.startNS = int64(100_000_000 * v)
		vp.emptyEditNS = 50_000_000
		// A genuine 8 ms skew stays well inside the 40 ms budget but is large
		// enough to be measured rather than assumed away.
		vp.audioDelayNS = 8_000_000
		if v == 1 {
			// The second rendition ships an unusable chunk-offset table and
			// must be recovered by scanning the sample table and media data.
			vp.badIndex = true
		}
		if v >= 2 {
			// Later renditions drop their tail, producing itemised missing
			// coverage against the earlier ones.
			vp.dropTailNS = int64(o.Duration) * 1_000_000_000 / 40
		}
		main.versions = append(main.versions, vp)
	}

	probe := assetPlan{name: "conflict-probe"}
	for v := 0; v < 2; v++ {
		vp := versionPlan{id: fmt.Sprintf("p%d", v+1), corruptSummaryAt: noCorruptCell}
		// Both probe versions are explicitly ranked, so the manifest has a
		// defined import and the conflict is resolved by the higher rank.
		vp.priority = intPtr([]int{20, 10}[v])
		// Identical slicing keeps the disagreement on exactly one interval.
		vp.sliceCells = 1
		vp.tracks = append(vp.tracks, avTracks(o)...)
		if v == 1 {
			// One cell's content digest is changed, so the versions conflict
			// on exactly one interval per track and nowhere else.
			vp.corruptSummaryAt = 1
		}
		probe.versions = append(probe.versions, vp)
	}
	return []assetPlan{main, probe}
}

// avTracks builds the video+audio pair for the demo duration. Video has a
// sparse 500 ms keyframe cadence and audio a different timescale, so the
// common timeline has to reconcile both.
func avTracks(o Options) []trackPlan {
	seconds := int64(o.Duration)
	// 50 ms grid: 4500 ticks at 90 kHz, 2400 at 48 kHz.
	video := trackPlan{
		kind: "video", trackID: "1", id: 1, handler: "vide",
		timescale: 90_000, delta: 4_500, cellTicks: 4_500,
		count: int(seconds * 20), size: 480, keyEvery: 10,
	}
	audio := trackPlan{
		kind: "audio", trackID: "2", id: 2, handler: "soun",
		timescale: 48_000, delta: 2_400, cellTicks: 2_400,
		count: int(seconds * 20), size: 64,
	}
	return []trackPlan{video, audio}
}

// nsToTicks converts a nanosecond offset to the track timescale, requiring
// grid alignment so published ranges never straddle a summary cell.
func nsToTicks(ns int64, tp trackPlan) (int64, error) {
	ticks, err := model.MulDiv(ns, int64(tp.timescale), 1_000_000_000)
	if err != nil {
		return 0, err
	}
	if ticks%tp.cellTicks != 0 {
		return 0, fmt.Errorf("demo: %dns is not a whole %d-tick cell at %d Hz", ns, tp.cellTicks, tp.timescale)
	}
	return ticks, nil
}

// nsToPresentationTicks converts a presentation offset to ticks without
// requiring grid alignment: empty edits shift the presentation timeline and do
// not alter which media cells exist.
func nsToPresentationTicks(ns int64, tp trackPlan) (int64, error) {
	ticks, err := model.MulDiv(ns, int64(tp.timescale), 1_000_000_000)
	if err != nil {
		return 0, err
	}
	if ticks*1_000_000_000 != ns*int64(tp.timescale) {
		return 0, fmt.Errorf("demo: %dns has no exact %d Hz tick representation", ns, tp.timescale)
	}
	return ticks, nil
}

// trimTicks returns the tick count remaining after dropTailNS.
func trimTicks(tp trackPlan, vp versionPlan) (int64, error) {
	total := int64(tp.count) * tp.delta
	drop, err := nsToTicks(vp.dropTailNS, tp)
	if err != nil {
		return 0, err
	}
	end := total - drop
	end = end / tp.cellTicks * tp.cellTicks
	if end <= 0 {
		return 0, fmt.Errorf("demo: track %s has no content after trimming", tp.trackID)
	}
	return end, nil
}

// manifestFor describes the generated files. Ranges are sliced differently
// per version, but each carries the per-grid-cell content digests it covers,
// so content can be compared across slicing choices.
func manifestFor(o Options, a assetPlan) (*manifest.Manifest, error) {
	m := &manifest.Manifest{Seed: o.Seed, Window: o.Window}
	ma := manifest.Asset{Name: a.name}
	for _, vp := range a.versions {
		mv := manifest.Version{ID: vp.id, Priority: vp.priority}
		for _, tp := range vp.tracks {
			end, err := trimTicks(tp, vp)
			if err != nil {
				return nil, model.New(model.KindInternal, model.StagePipeline, a.name, "plan", err).
					WithVersion(vp.id)
			}
			startTicks, err := nsToTicks(vp.startNS, tp)
			if err != nil {
				return nil, model.New(model.KindInternal, model.StagePipeline, a.name, "plan", err).
					WithVersion(vp.id)
			}
			slice := trackSlice(tp, versionIndex(a, vp), vp.sliceCells)
			var ranges []manifest.Range
			for start := int64(0); start < end; start += slice {
				dur := slice
				if start+dur > end {
					dur = end - start
				}
				firstCell := start / tp.cellTicks
				cellCount := dur / tp.cellTicks
				chunks := media.RangeChunks(a.name, tp.id, firstCell, cellCount)
				if vp.corruptSummaryAt >= 0 && vp.corruptSummaryAt >= firstCell && vp.corruptSummaryAt < firstCell+cellCount {
					chunks[vp.corruptSummaryAt-firstCell] = media.CorruptSummary(chunks[vp.corruptSummaryAt-firstCell])
				}
				ranges = append(ranges, manifest.Range{
					StartTicks:    startTicks + start,
					DurationTicks: dur,
					Summary:       media.CombineChunks(chunks),
					Chunks:        chunks,
					Samples:       int(dur / tp.delta),
					Bytes:         int64(dur/tp.delta) * int64(tp.size),
					Sparse:        tp.kind == "video",
					GopTicks:      int64(tp.keyEvery) * tp.delta,
				})
			}
			mv.Tracks = append(mv.Tracks, manifest.Track{
				ID: tp.trackID, Kind: tp.kind,
				Timescale:     tp.timescale,
				EditMediaTime: startTicks,
				Ranges:        ranges,
			})
		}
		ma.Versions = append(ma.Versions, mv)
	}
	m.Assets = []manifest.Asset{ma}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func versionIndex(a assetPlan, vp versionPlan) int {
	for i, v := range a.versions {
		if v.id == vp.id {
			return i
		}
	}
	return 0
}

// fileSpec builds the MP4 generator input for one track of one version.
func fileSpec(a assetPlan, vp versionPlan, tp trackPlan) (media.FileSpec, error) {
	end, err := trimTicks(tp, vp)
	if err != nil {
		return media.FileSpec{}, err
	}
	count := int(end / tp.delta)
	startTicks, err := nsToTicks(vp.startNS, tp)
	if err != nil {
		return media.FileSpec{}, err
	}
	emptyNS := vp.emptyEditNS
	if tp.kind == "audio" {
		emptyNS += vp.audioDelayNS
	}
	emptyTicks, err := nsToPresentationTicks(emptyNS, tp)
	if err != nil {
		return media.FileSpec{}, err
	}
	track := media.TrackSpec{
		ID: tp.id, Handler: tp.handler, Timescale: tp.timescale,
		StartTicks: startTicks, EditMedia: startTicks,
		EditDur:        int64(count) * tp.delta,
		EmptyEditTicks: emptyTicks,
		Samples: media.SampleSpec{
			Count: count, Size: tp.size, Delta: uint32(tp.delta), KeyEvery: tp.keyEvery,
		},
	}
	return media.FileSpec{Asset: a.name + "/" + vp.id, Tracks: []media.TrackSpec{track}}, nil
}

// trackSlice is the publication slice length in ticks for a version. An
// explicit cell count wins; otherwise the per-version pattern keeps the
// default asset ambiguous about boundaries on purpose.
func trackSlice(tp trackPlan, v int, cells int64) int64 {
	if cells > 0 {
		return tp.cellTicks * cells
	}
	return tp.cellTicks * []int64{2, 1, 4}[v%3]
}
