package demo

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/medialab/medialab/internal/bitrate"
	"github.com/medialab/medialab/internal/clock"
	"github.com/medialab/medialab/internal/manifest"
	"github.com/medialab/medialab/internal/render"
)

// writeManifestArtifact persists the manifest and, for the truncated fault,
// cuts the document in half so parsing fails with a location error.
func writeManifestArtifact(cfg Config, m *manifest.Manifest) error {
	var buf bytes.Buffer
	if err := m.Write(&buf); err != nil {
		return err
	}
	raw := buf.Bytes()
	// Truncation is a standalone input fault. The composite "corrupt" case
	// keeps a valid manifest so faults can flow through recovery, synthesis
	// and reporting rather than aborting at parse time.
	if cfg.Fault == FaultTruncated {
		raw = raw[:len(raw)/2]
	}
	path := filepath.Join(cfg.OutDir, "manifest.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	// Re-parse the artifact exactly as a downstream node would.
	if _, err := manifest.Parse(bytes.NewReader(raw)); err != nil {
		return err
	}
	// Wrap the intact manifest in a parser-verifiable MP4 evidence container.
	mp4Path := filepath.Join(cfg.OutDir, "evidence.mp4")
	f, err := os.Create(mp4Path)
	if err != nil {
		return err
	}
	if err := WriteEvidenceMP4(f, buf.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}

// rateResult summarizes the bitrate controller run.
type rateResult struct {
	target int64
	actual int64
	delta  int
}

// runBitrate feeds the generated trace through the slew-limited controller.
func runBitrate(cfg Config, fx *Fixture) rateResult {
	ctl, err := bitrate.NewController(bitrate.Config{
		TargetBps:       cfg.TargetBps,
		MinBps:          cfg.TargetBps / 10,
		SlewUpPct:       25,
		SlewDownPct:     25,
		EWMAAlpha:       0.5,
		InterruptTicks:  500,
		TimeoutTicks:    2_000,
		ChunkDurationMs: tickStep,
	})
	if err != nil {
		return rateResult{target: cfg.TargetBps, actual: 0, delta: 100}
	}
	var last bitrate.State
	for i, bps := range fx.BandwidthTrace {
		at := int64(i) * 1000
		_ = ctl.Observe(bitrate.Sample{AtTick: at, BandwidthBps: bps})
		last = ctl.Decide(at)
	}
	if len(fx.BandwidthTrace) == 0 {
		last = ctl.Decide(0)
	}
	return rateResult{
		target: last.TargetBps,
		actual: last.ActualBps,
		delta:  last.DeltaPercent(),
	}
}

// renderFrames exports one evidence frame per recovered video sample. Audio
// samples advance the timeline but do not produce raster frames. On a render
// write failure the frames drawn so far are still returned, so the summary
// can report how many frames were recovered before export failed.
func renderFrames(ctx context.Context, cfg Config, tracks []recoveredTrack,
	tl *clock.Timeline,
) ([]render.Frame, error) {
	opener := render.FSWriter
	injectFailure := cfg.Fault == FaultRender || cfg.Fault == FaultComposite
	r := render.NewRenderer(render.Config{OutDir: cfg.OutDir}, opener)
	var frames []render.Frame
	var renderErr error
	index := 0
	for _, t := range tracks {
		if t.trackID != "v0" {
			continue
		}
		for _, s := range t.samples {
			select {
			case <-ctx.Done():
				return frames, ctx.Err()
			default:
			}
			f := r.Draw(s.TrackID, s.AtTick, index, s.WaitingKF)
			frames = append(frames, f) // recovered frame is recorded first
			if renderErr == nil {
				wo := render.FSWriter
				if injectFailure && index == 5 {
					wo = failingOpener()
				}
				r.SetOpener(wo)
				if err := r.Write(f, r.Raster(f)); err != nil && renderErr == nil {
					renderErr = err
				}
			}
			index++
		}
	}
	return frames, renderErr
}

func qualities(frames []render.Frame) []float64 {
	out := make([]float64, len(frames))
	for i, f := range frames {
		out[i] = f.Quality
	}
	return out
}

// alignmentError computes the worst distance between any track point and the
// nearest point on the video track. Ordinal-position comparison would be
// shifted by gaps and duplicates, so the comparison is made on the actual
// tick timeline instead.
func alignmentError(tl *clock.Timeline) int64 {
	points := tl.Points()
	var videoTicks, otherTicks []int64
	for _, p := range points {
		if p.Track == "v0" {
			videoTicks = append(videoTicks, p.At)
		} else {
			otherTicks = append(otherTicks, p.At)
		}
	}
	var worst int64
	for _, at := range otherTicks {
		if d := nearestDistance(videoTicks, at); d > worst {
			worst = d
		}
	}
	return worst
}

// nearestDistance returns |at - nearest(v)| for a sorted tick slice v.
func nearestDistance(v []int64, at int64) int64 {
	if len(v) == 0 {
		return 0
	}
	lo, hi := 0, len(v)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if v[mid] < at {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	d := v[lo] - at
	if d < 0 {
		d = -d
	}
	if lo > 0 {
		prev := at - v[lo-1]
		if prev < d {
			d = prev
		}
	}
	return d
}

// failingOpener returns a writer that rejects every write, modeling a render
// destination that runs out of space mid-export.
func failingOpener() render.WriterOpener {
	return func(path string) (io.WriteCloser, error) {
		return errWriteCloser{}, nil
	}
}

type errWriteCloser struct{}

func (errWriteCloser) Write([]byte) (int, error) {
	return 0, os.ErrPermission
}
func (errWriteCloser) Close() error { return nil }
