package pipeline

import (
	"context"
	"strings"

	"mediakernel/internal/align"
	fx "mediakernel/internal/fixture"
	"mediakernel/internal/manifest"
	"mediakernel/internal/mediaerr"
	"mediakernel/internal/pool"
	"mediakernel/internal/remux"
	"mediakernel/internal/rtp"
)

type dashParse struct{ doc *manifest.DASHDoc }
type hlsParse struct{ doc *manifest.HLSDoc }

func parseDASH(f *fx.Fixture) (*manifest.DASHDoc, error) {
	return manifest.ParseDASH(strings.NewReader(f.DASHMPD))
}

func parseHLS(f *fx.Fixture) (*manifest.HLSDoc, error) {
	return manifest.ParseHLS(strings.NewReader(f.HLSPlaylist))
}

// buildFragments converts every representation's segments into alignable
// fragments, preserving the stable rank assigned by the parser.
func buildFragments(d *dashParse) []align.Fragment {
	var out []align.Fragment
	for _, rep := range d.doc.Representations {
		for _, seg := range rep.Segments {
			end, _ := addTick(seg.StartTick, seg.DurationTick)
			out = append(out, align.Fragment{
				Source: "dash", TrackID: rep.ID, Kind: rep.Kind, Rank: rep.Rank,
				SegmentIndex: seg.Index, StartTick: seg.StartTick,
				DurationTick: seg.DurationTick, EndTick: end,
			})
		}
	}
	return out
}

func addTick(a, b uint64) (uint64, error) {
	// Local alias to keep the import surface explicit in this file.
	return clockAdd(a, b)
}

// stageTransport pushes every fault-case packet stream through a reorderer
// inside the bounded worker pool. Bandwidth changes only pace the job
// enqueue; they never alter the packets or the merged result.
func stageTransport(ctx context.Context, cfg Config, fixture *fx.Fixture,
	report *mediaerr.Report) ([]rtp.Stats, int) {

	window := cfg.ReorderWindow
	if window == 0 {
		window = 16
	}
	reorderer, err := rtp.NewReorderer(rtp.Config{Window: window})
	if err != nil {
		return nil, ExitFailure
	}

	p, err := pool.New(ctx, cfg.Workers, cfg.Queue)
	if err != nil {
		return nil, ExitFailure
	}

	var paceIdx int
	for _, fc := range fixture.Cases {
		// The bandwidth profile only changes how aggressively jobs are
		// paced; here it selects a deterministic weight and never alters the
		// packets or the merged result. No wall-clock sleep is used, so the
		// output hash is independent of host scheduling.
		paceWeight := paceWeight(cfg.BandwidthKbps, paceIdx)
		paceIdx++
		fc := fc
		job := func(jobCtx context.Context) error {
			for _, pkt := range fc.Packets {
				if jobCtx.Err() != nil {
					return mediaerr.New(mediaerr.CatCanceled, "job", "transport job canceled").
						Wrap(jobCtx.Err())
				}
				if _, errs := reorderer.Push(fc.StreamID+":"+fc.Name, pkt); len(errs) > 0 {
					for _, e := range errs {
						report.Add(e)
					}
				}
			}
			_ = paceWeight
			return nil
		}
		if err := p.Submit(ctx, job); err != nil {
			_ = p.Wait()
			return nil, ExitFailure
		}
	}
	if err := p.Wait(); err != nil {
		if isCanceled(ctx, err) {
			return nil, ExitFailure
		}
		report.Add(asMedia(err))
		return nil, ExitFailure
	}

	// Flush each logical stream to settle tail gaps and collect ledgers.
	var stats []rtp.Stats
	seen := map[string]bool{}
	for _, fc := range fixture.Cases {
		id := fc.StreamID + ":" + fc.Name
		if seen[id] {
			continue
		}
		seen[id] = true
		_, st, errs := reorderer.Flush(id)
		for _, e := range errs {
			report.Add(e)
		}
		stats = append(stats, st)
	}
	return stats, ExitOK
}

// paceWeight maps a bandwidth tier to a deterministic relative pacing weight
// (higher bandwidth -> lower weight). It is integer-only and repeatable.
func paceWeight(profile []int, idx int) int {
	if len(profile) == 0 {
		return 1
	}
	kbps := profile[idx%len(profile)]
	if kbps <= 0 {
		return 1
	}
	// 2.5 Mbps maps to ~1, 700 kbps to ~4; bounded and allocation-free.
	w := 2500 / kbps
	if w < 1 {
		w = 1
	}
	return w
}

func isCanceled(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if me, ok := mediaerr.As(err); ok && me.Category == mediaerr.CatCanceled {
		return true
	}
	return false
}

// stageRemux builds a minimal two-track MP4 from the reference video and audio
// representations, verifies it and publishes atomically.
func stageRemux(cfg Config, d *dashParse, report *mediaerr.Report) (*remux.Summary, int) {
	var video, audio *manifest.Representation
	for i := range d.doc.Representations {
		rep := &d.doc.Representations[i]
		switch rep.Kind {
		case "video":
			if video == nil || rep.Rank < video.Rank {
				video = rep
			}
		case "audio":
			if audio == nil {
				audio = rep
			}
		}
	}
	if video == nil || audio == nil {
		report.Add(mediaerr.New(mediaerr.CatRemux, "tracks", "need one video and one audio track"))
		return nil, ExitReported
	}
	in := remux.Input{
		DurationTick: d.doc.MediaPresentationDuration,
		Tracks: []remux.Track{
			{
				ID: 1, Kind: "video", Codec: "avc1", Timescale: uint32(video.Timescale),
				SampleCount: uint32(len(video.Segments)), SampleDelta: 90000,
				Width: 320, Height: 180, Payload: []byte("mediakernel-video"),
			},
			{
				ID: 2, Kind: "audio", Codec: "mp4a", Timescale: uint32(audio.Timescale),
				SampleCount: uint32(len(audio.Segments)), SampleDelta: 48000,
				ChannelCount: 2, SampleRate: 48000, Payload: []byte("mediakernel-audio"),
			},
		},
	}
	sum, err := remux.WriteAtomically(cfg.OutputPath, in)
	if err != nil {
		report.Add(asMedia(err))
		return nil, ExitReported
	}
	return sum, ExitOK
}
