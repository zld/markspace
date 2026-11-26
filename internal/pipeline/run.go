package pipeline

import (
	"fmt"
	"path/filepath"

	"continuity/internal/fixture"
	"continuity/internal/journal"
	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/migrate"
	"continuity/internal/render"
	rtprec "continuity/internal/rtp"
	"continuity/internal/timeline"
)

// Options configures one deterministic continuity run.
type Options struct {
	Case        string
	Root        string
	Out         string
	InjectCrash bool
}

// Result contains all subsystem reports and publication records.
type Result struct {
	Spec      *fixture.Spec
	Bundle    *manifest.Bundle
	RTP       *rtprec.Report
	Timeline  *timeline.Result
	Render    *render.Result
	Migration *migrate.Report
	Journal   *journal.Report
}

// Run generates/loads the requested sample, parses inputs, recovers RTP,
// composes the timeline, renders PNG frames, migrates the catalog, and commits
// through the write-ahead journal.
func Run(opts Options) (*Result, error) {
	if opts.Case == "" {
		return nil, media.NewFault(media.PhaseCLI, media.CodeInvalidArgument, "--case is required")
	}
	root := opts.Root
	if root == "" {
		if opts.Out == "" {
			return nil, media.NewFault(media.PhaseCLI, media.CodeInvalidArgument, "either --root or --out is required")
		}
		root = filepath.Join(opts.Out, "sample")
	}
	spec, err := fixture.Build(opts.Case, root)
	if err != nil {
		return nil, err
	}
	res := &Result{Spec: spec}

	bundle, err := parseEntry(spec)
	if err != nil {
		return res, err
	}
	res.Bundle = bundle

	samples, err := samplesFromBundle(spec, bundle)
	if err != nil {
		return res, err
	}

	if spec.RTPPath != "" {
		pkts, rerr := rtprec.ReadRecords(spec.RTPPath)
		if rerr != nil {
			return res, rerr
		}
		res.RTP = rtprec.RecoverPackets(pkts)
		if res.RTP.Unrecoverable > 0 {
			return res, &media.Fault{Phase: media.PhaseRTP, Code: media.CodeUnrecoverableLoss,
				Source: spec.RTPPath, Detail: fmt.Sprintf("%d RTP packets unrecoverable", res.RTP.Unrecoverable)}
		}
	}

	tl, err := timeline.Compose(samples, timeline.Options{})
	res.Timeline = tl
	if err != nil {
		return res, err
	}
	rr, err := render.RenderAll(tl)
	res.Render = rr
	if err != nil {
		return res, err
	}

	if spec.Catalog != "" {
		mr, merr := migrate.MigrateFile(spec.Catalog, migrate.Options{})
		if merr != nil {
			return res, merr
		}
		res.Migration = mr
	}

	if opts.Out != "" {
		arts, frameRecords := journalArtifacts(rr)
		sources := []string{spec.Entry, spec.RTPPath}
		jr, jerr := journal.Commit(opts.Out, opts.Case, opts.Case+"-tx", arts, frameRecords, sources, opts.InjectCrash)
		res.Journal = jr
		if jerr != nil {
			return res, jerr
		}
	}
	return res, nil
}

func parseEntry(spec *fixture.Spec) (*manifest.Bundle, error) {
	switch spec.Format {
	case "dash":
		return manifest.ParseDASH(spec.Entry, spec.Root)
	default:
		return manifest.ParseHLSBundle(spec.Entry, spec.Root)
	}
}

func samplesFromBundle(spec *fixture.Spec, b *manifest.Bundle) ([]timeline.TrackSample, error) {
	var out []timeline.TrackSample
	for _, seg := range b.Segments {
		timescale := spec.VideoTimescale
		if seg.Kind == "audio" {
			timescale = spec.AudioTimescale
		}
		frag, err := media.ReadFragment(seg.AbsPath, timescale)
		if err != nil {
			return nil, err
		}
		for _, smp := range frag.Samples {
			out = append(out, timeline.TrackSample{
				Track: seg.Track, Kind: seg.Kind, PTSNS: smp.PTSNS, DurationNS: smp.DurationNS,
				Source: seg.RelPath, Sequence: seg.Sequence, ArrivalIndex: seg.Index,
				Switch: seg.Discontinuity && smp.Index == 0,
			})
		}
	}
	return out, nil
}

func journalArtifacts(rr *render.Result) ([]journal.Artifact, []journal.FrameRecord) {
	arts := make([]journal.Artifact, 0, len(rr.Frames)+1)
	rows := make([]journal.FrameRecord, 0, len(rr.Frames))
	for _, f := range rr.Frames {
		name := fmt.Sprintf("frames/%04d.png", f.Sequence)
		f.Path = name
		arts = append(arts, journal.Artifact{Name: name, Data: f.PNGBytes, SHA: f.SHA256})
		rows = append(rows, journal.FrameRecord{Sequence: f.Sequence, File: name,
			PTSNS: f.PTSNS, SHA256: f.SHA256, Sources: f.Sources})
	}
	arts = append(arts, journal.ChecksumSummary(arts))
	return arts, rows
}
