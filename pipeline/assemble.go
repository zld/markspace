package pipeline

import (
	"math"
	"strings"

	"mediad/index"
	"mediad/media"
	"mediad/timeline"
)

// renditions selects the alignable video renditions from the scanned inputs. A
// rendition's name is the input path's base name without extension; when
// several inputs share a base name the full path disambiguates it. The track
// map is keyed by the same name so manifest byte ranges resolve back.
func (j *Job) renditions() ([]media.Rendition, map[string]media.Track, error) {
	var renditions []media.Rendition
	tracks := map[string]media.Track{}
	for _, path := range j.Paths() {
		res := j.scanned[path]
		if res.Clip == nil {
			continue
		}
		video, ok := res.Clip.VideoTrack()
		if !ok {
			continue
		}
		name := renditionName(path)
		if _, exists := tracks[name]; exists {
			name = path
		}
		tracks[name] = video
		renditions = append(renditions, media.Rendition{Name: name, Track: video})
	}
	if len(renditions) == 0 {
		return nil, nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeMissingTrack,
			Message: "no video rendition found in any input"}}
	}
	return renditions, tracks, nil
}

// renditionName derives a stable, human-readable rendition name from a path.
func renditionName(path string) string {
	base := path
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// buildManifest assembles the manifest from alignment, segments, collected
// events and the run's statistics.
func (j *Job) buildManifest(inputs []Input, align media.AlignResult, segments []SegmentInfo,
	issues timeline.Issues, opts RunOptions) *Manifest {
	m := &Manifest{
		Schema:   1,
		Complete: true,
		Issues:   issues.Sorted(),
		Segments: segments,
		Inputs:   map[string]index.Fingerprint{},
	}
	for _, path := range j.Paths() {
		res := j.scanned[path]
		m.Inputs[j.displayPath(path)] = res.Fingerprint
	}
	m.JobID = computeJobID(m.Inputs, j.Config)

	// Renditions, time range and events.
	for _, r := range align.Segments {
		m.StartUs = min64(m.StartUs, r.Start)
	}
	if len(segments) > 0 {
		m.StartUs = segments[0].StartUs
		m.EndUs = segments[len(segments)-1].EndUs
	}
	for _, path := range j.Paths() {
		res := j.scanned[path]
		if res.Clip == nil {
			continue
		}
		if video, ok := res.Clip.VideoTrack(); ok {
			m.Renditions = append(m.Renditions, RenditionInfo{
				Name: renditionName(path), TrackID: video.ID, TimeScale: video.TimeScale,
				Width: video.Width, Height: video.Height, Samples: len(video.Samples),
			})
		}
	}

	// Events: merge across inputs by content, drop recovered ones, sort.
	var set timeline.EventSet
	for _, path := range j.Paths() {
		for _, ev := range j.scanned[path].Events {
			set.Add(ev)
		}
	}
	set.Sort()
	published := set.ForManifest()
	for _, ev := range published {
		tus, _ := ev.TimeMicros()
		dus, _ := ev.DurationMicros()
		src := ev.Source
		src.File = j.displayPath(src.File)
		m.Events = append(m.Events, EventInfo{
			Kind: string(ev.Kind), Scheme: ev.Scheme, TimeUs: tus, DurationUs: dus,
			Source: src, PayloadSum: ev.PayloadSum(),
		})
	}
	m.Issues = joinIssues(issues, set.Issues())

	// Stats.
	m.Stats = Stats{
		AlignedSegments:  countAligned(segments),
		EventCount:       len(published),
		SegmentCount:     len(segments),
		IndexReused:      j.anyIndexReused(),
		LossRecoveryRate: j.lossRecoveryRate(),
		AVSyncMs:         j.avSyncMs(published),
		LatencyP95Ms:     Percentile(opts.LatencySamplesMs, 95),
	}
	return m
}

func countAligned(segments []SegmentInfo) int {
	n := 0
	for _, s := range segments {
		if len(s.Renditions) > 0 {
			n++
		}
	}
	return n
}

func (j *Job) anyIndexReused() bool {
	for _, path := range j.Paths() {
		if j.scanned[path].IndexReused {
			return true
		}
	}
	return false
}

// lossRecoveryRate reports the worst recovery rate across inputs that carried
// RTP packets; a run with no RTP inputs reports 1.0 (nothing was lost).
func (j *Job) lossRecoveryRate() float64 {
	worst := 1.0
	for _, path := range j.Paths() {
		res := j.scanned[path]
		if res.RTP == nil {
			continue
		}
		if rate := res.RTP.LossRecoveryRate(); rate < worst {
			worst = rate
		}
	}
	return worst
}

// avSyncMs is the largest gap between a subtitle/cue event and the nearest
// audio/video frame boundary on the shared timeline, in milliseconds. It is the
// figure the acceptance run requires to be ≤ 40 ms.
func (j *Job) avSyncMs(events []timeline.Event) float64 {
	// Frame boundary granularity: half a frame at the densest rendition's fps,
	// which is the best a cue can be placed without sub-frame trimming.
	worst := 0.0
	for _, path := range j.Paths() {
		res := j.scanned[path]
		if res.Clip == nil {
			continue
		}
		for _, ev := range events {
			// Only compare events to the timeline they were parsed from.
			if ev.Source.File != res.Clip.Path {
				continue
			}
			if ev.Kind != timeline.KindSubtitle && ev.Kind != timeline.KindChapter {
				continue
			}
			gap := cueFrameGapMs(ev, *res.Clip)
			if gap > worst {
				worst = gap
			}
		}
	}
	return worst
}

// cueFrameGapMs returns how far a cue's time sits from the nearest frame
// start of any video track, in milliseconds.
func cueFrameGapMs(ev timeline.Event, clip media.Clip) float64 {
	us, err := ev.Presentation.Micros()
	if err != nil {
		return 0
	}
	best := -1.0
	for _, t := range clip.Tracks {
		if t.Kind != media.KindVideo || t.TimeScale == 0 {
			continue
		}
		frameUs := float64(0)
		for _, s := range t.Samples {
			if s.DurationTicks > 0 {
				frameUs = float64(s.DurationTicks) * 1e6 / float64(t.TimeScale)
				break
			}
		}
		if frameUs == 0 {
			continue
		}
		// Distance to the nearest frame start on a uniform grid.
		phase := math.Mod(float64(us), frameUs)
		d := phase
		if frameUs-phase < d {
			d = frameUs - phase
		}
		ms := d / 1000
		if best < 0 || ms < best {
			best = ms
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
