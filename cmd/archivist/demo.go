package main

import (
	"fmt"
	"time"

	"archivist/internal/fixture"
	"archivist/internal/index"
	"archivist/internal/pipeline"
)

func timeNow() time.Time { return time.Now() }

// runQueries resolves representative times to keyframes for every healthy
// sample. Tolerance is one sample tick of the track timescale.
func runQueries(results []*pipeline.InputResult, samples []*fixture.Sample) ([]queryReport, error) {
	byKind := make(map[string]*fixture.Sample, len(samples))
	for _, s := range samples {
		byKind[s.Kind] = s
	}
	var out []queryReport
	for _, res := range results {
		if res.Status != "ok" || res.IndexPath == "" {
			continue
		}
		ix, err := index.Load(res.IndexPath)
		if err != nil {
			return nil, err
		}
		sample, ok := byKind[res.Name]
		if !ok {
			continue
		}
		step := int64(sample.MediaTimescale) / 30
		if step <= 0 {
			step = 1
		}
		for _, track := range ix.Tracks {
			first := track.Entries[0].PTS
			last := track.Entries[len(track.Entries)-1].PTS
			if last <= first {
				continue
			}
			// Tolerance is one sample tick of this track, measured from the
			// actual timeline rather than assumed from the fixture.
			tolerance := track.Step()
			if tolerance <= 0 {
				tolerance = step
			}
			// Three probes: start, two thirds, and a point that deliberately
			// falls between keyframes.
			targets := []int64{
				first,
				first + (last-first)*2/3,
				first + (last-first)/2 + tolerance/3,
			}
			for _, target := range targets {
				loc, err := ix.Locate(track.ID, target)
				if err != nil {
					return nil, err
				}
				out = append(out, queryReport{
					Sample:        res.Name,
					TrackID:       track.ID,
					Target:        target,
					Keyframe:      loc.KeyframeSample,
					KeyPTS:        loc.KeyframePTS,
					Delta:         loc.Delta,
					KeyframeDelta: loc.KeyframeDelta,
					Tolerance:     tolerance,
					OK:            loc.Available && loc.Delta >= 0 && loc.Delta <= tolerance,
				})
			}
		}
	}
	return out, nil
}

// verifyQueryReports returns an error if any lookup missed tolerance.
func verifyQueryReports(reports []queryReport) error {
	for _, q := range reports {
		if !q.OK {
			return fmt.Errorf("query on %s at %d resolved to keyframe %d (pts %d, delta %d) beyond tolerance %d",
				q.Sample, q.Target, q.Keyframe, q.KeyPTS, q.Delta, q.Tolerance)
		}
	}
	return nil
}
