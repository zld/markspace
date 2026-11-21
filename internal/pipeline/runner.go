package pipeline

import (
	"fmt"
	"math"
	"path/filepath"

	"hlsflow/internal/config"
	"hlsflow/internal/integrity"
	"hlsflow/internal/m3u8"
	"hlsflow/internal/safeio"
	"hlsflow/internal/ts"
)

// Build runs the full ingest for every rendition in job and publishes media
// playlists, the master playlist and the integrity index under outputRoot.
// It returns a report even when some inputs are faulty; hard configuration
// errors are returned as an error.
func Build(job *config.Job, inputRoot, outputRoot *safeio.Root, opts Options) (*Report, error) {
	targetTicks := int64(opts.TargetDuration.Seconds() * ts.TicksPerSecond)
	gap := config.Duration{Duration: opts.GapTimeout}
	if gap.Duration == 0 {
		gap = job.GapTimeout
	}
	index := integrity.New()
	report := &Report{job: job}

	var built []*builtRendition
	for _, rd := range job.Renditions {
		plPath := filepath.Join(rd.Name, "index.m3u8")
		// During ingest the playlist stays open (EVENT); it is only closed in
		// the final write below when --finalize is requested.
		publish := func(pl m3u8.MediaPlaylist, idx *integrity.Index) error {
			pl.Closed = false
			if err := outputRoot.AtomicWrite(plPath, pl.Bytes()); err != nil {
				return err
			}
			return idx.Save(outputRoot)
		}
		br, failures, err := processRendition(rd, inputRoot, outputRoot, targetTicks, gap, index, publish)
		if err != nil {
			return nil, fmt.Errorf("build rendition %q: %w", rd.Name, err)
		}
		line := report.ensureRendition(rd)
		*line = br.report
		for _, f := range failures {
			report.addFailure(rd.Name, f)
		}
		br.playlist.Closed = opts.Finalize
		if err := outputRoot.AtomicWrite(plPath, br.playlist.Bytes()); err != nil {
			return nil, err
		}
		if err := index.Save(outputRoot); err != nil {
			return nil, err
		}
		built = append(built, br)
	}

	applyDrift(report, built, job.DriftTolerance.Duration.Seconds())

	master := m3u8.MasterPlaylist{}
	for _, rd := range job.Renditions {
		master.Variants = append(master.Variants, m3u8.Variant{
			URI:       filepath.Join(rd.Name, "index.m3u8"),
			Bandwidth: rd.Bitrate,
			Name:      rd.Name,
			Codecs:    "avc1.4d401f,mp4a.40.2",
		})
	}
	if err := outputRoot.AtomicWrite("master.m3u8", master.Bytes()); err != nil {
		return nil, err
	}
	if err := index.Save(outputRoot); err != nil {
		return nil, err
	}
	return report, nil
}

// applyDrift compares every rendition's segment boundaries against the first
// rendition's shared timeline and records drift beyond the tolerance.
func applyDrift(report *Report, built []*builtRendition, tolerance float64) {
	if len(built) < 2 {
		return
	}
	reference := built[0]
	for i := 1; i < len(built); i++ {
		cand := built[i]
		var maxDrift float64
		n := len(reference.boundaries)
		if len(cand.boundaries) < n {
			n = len(cand.boundaries)
		}
		for j := 0; j < n; j++ {
			d := cand.boundaries[j] - reference.boundaries[j]
			if math.Abs(d) > math.Abs(maxDrift) {
				maxDrift = d
			}
		}
		for k := range report.Renditions {
			if report.Renditions[k].Rendition == cand.report.Rendition {
				report.Renditions[k].DriftSeconds = round3(maxDrift)
				if tolerance > 0 && math.Abs(maxDrift) > tolerance {
					report.addFailure(cand.report.Rendition, Failure{
						Rendition: cand.report.Rendition,
						Segment:   n - 1,
						Reason:    fmt.Sprintf("timeline drift %.3fs exceeds tolerance %.3fs", maxDrift, tolerance),
					})
				}
			}
		}
	}
}
