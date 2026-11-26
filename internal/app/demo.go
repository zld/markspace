package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"mediaprep/internal/fixture"
	"mediaprep/internal/manifest"
	"mediaprep/internal/manifest/migrate"
	"mediaprep/internal/metadata"
	"mediaprep/internal/model"
	"mediaprep/internal/process"
	"mediaprep/internal/process/fingerprint"
	"mediaprep/internal/source"
	"mediaprep/internal/stream"
)

type DemoOptions struct {
	Root           string
	Generate       bool
	Queue          int
	BudgetBytes    int64
	SlowConsumerMS int
}

func RunDemo(ctx context.Context, opts DemoOptions) (*manifest.Document, error) {
	if opts.Root == "" {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1, "--root is required")
	}
	var files *fixture.DemoFiles
	var err error
	if opts.Generate {
		files, err = fixture.GenerateDemoFiles(opts.Root)
		if err != nil {
			return nil, err
		}
	} else {
		files = &fixture.DemoFiles{
			Root:      opts.Root,
			MainVideo: filepath.Join(opts.Root, "media", "video-main.mp4"),
			Duplicate: filepath.Join(opts.Root, "media", "video-duplicate.mp4"),
			AudioMXF:  filepath.Join(opts.Root, "media", "audio.mxf"),
			CutTicks:  []int64{12000, 33600},
		}
	}

	type parsed struct {
		rel  string
		file *source.MediaFile
	}
	paths := []string{files.MainVideo, files.Duplicate, files.AudioMXF}
	parsedFiles := make([]parsed, 0, len(paths))
	items := make([]fingerprint.SourcedTrack, 0)
	var exceptions []model.Exception
	var mainTrack, audioTrack *source.Track

	for _, path := range paths {
		mf, err := source.ParseFile(path)
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(opts.Root, path)
		parsedFiles = append(parsedFiles, parsed{rel: rel, file: mf})
		for _, tr := range mf.Tracks {
			resolved, err := metadata.ResolveTrack(tr)
			if err != nil {
				return nil, err
			}
			exceptions = append(exceptions, resolved.Conflicts...)
			items = append(items, fingerprint.SourcedTrack{Path: rel, Track: tr})
			if path == files.MainVideo && tr.Role == "video" {
				mainTrack = tr
			}
			if path == files.AudioMXF && tr.Role == "audio" {
				audioTrack = tr
			}
		}
	}
	if mainTrack == nil || audioTrack == nil {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseProcess, "", -1,
			"demo did not produce video and audio tracks")
	}

	groups := fingerprint.Dedup(items)
	duplicateCount := fingerprint.CountDuplicates(groups)

	cut, err := process.PlanCuts(mainTrack, process.CutRequest{
		Clock: process.DeliveryClock, CutTicks: files.CutTicks,
	})
	if err != nil {
		return nil, err
	}
	for _, snap := range cut.Snaps {
		reqShared := model.ConvertTicks(snap.RequestedTicks, snap.Clock, process.DeliveryClock)
		actualShared := model.ConvertTicks(snap.ActualTicks, snap.Clock, process.DeliveryClock)
		exceptions = append(exceptions, model.Exception{
			Kind: "cut-snap-to-next-sample", Phase: model.PhaseProcess, Track: mainTrack.Label,
			Interval: &model.Interval{
				Start: reqShared, End: actualShared, Scale: process.DeliveryClock.TickDuration,
			},
			Message: fmt.Sprintf("requested %.3f ms lies between samples; next boundary is %.3f ms (+%.3f ms)",
				snap.RequestedMS, snap.ActualMS, snap.ActualMS-snap.RequestedMS),
			Resolution: "cut at next sample; deviation recorded using shared rational clock",
		})
	}

	silences, err := process.DetectSilence(audioTrack, process.DefaultSilenceConfig())
	if err != nil {
		return nil, err
	}
	for _, s := range silences {
		iv := s.Interval
		exceptions = append(exceptions, model.Exception{
			Kind: "silence", Phase: model.PhaseProcess, Track: s.Track, Interval: &iv,
			Message:    fmt.Sprintf("RMS %s over %.1f ms, %d windows", s.RMSDBText, s.DurationMS, s.WindowCount),
			Resolution: "marked silent interval",
		})
	}
	for _, g := range groups {
		if len(g.Refs) > 1 {
			exceptions = append(exceptions, model.Exception{
				Kind: "duplicate-fingerprint", Phase: model.PhaseProcess, Track: g.Role,
				Message:    fmt.Sprintf("SHA-256 %s has %d sources with identical role/order bytes", g.Fingerprint[:16], len(g.Refs)),
				Resolution: "merged into one reference while preserving all source provenance",
			})
		}
	}

	metrics, err := runDemoPipeline(ctx, opts, mainTrack, audioTrack)
	if err != nil {
		return nil, err
	}

	doc := buildDocument(opts.Root, groups, cut.Segments, silences, exceptions, metrics)
	doc.Summary.Duplicates = duplicateCount
	doc.Summary.MetadataConflicts = countKind(exceptions, "metadata-conflict")
	doc.Summary.Segments = len(cut.Segments)
	doc.Summary.Silent = len(silences)
	doc.Summary.QueuePeak = metrics.QueuePeak
	doc.Summary.QueueCapacity = metrics.QueueCapacity
	doc.Summary.BufferPeakBytes = metrics.BufferPeakBytes
	doc.Summary.BufferBudgetBytes = metrics.BufferBudgetBytes
	doc.Summary.Verdict = judgeDemo(doc.Summary, metrics, opts.Queue, opts.BudgetBytes)
	doc.Pipeline = metrics

	if _, err := migrate.WriteInitialV1(opts.Root, doc, time.Now()); err != nil {
		return nil, err
	}
	return doc, nil
}

func runDemoPipeline(ctx context.Context, opts DemoOptions, tracks ...*source.Track) (stream.Metrics, error) {
	pipe, err := stream.New(stream.Options{
		QueueLength: opts.Queue, BufferBudgetBytes: opts.BudgetBytes,
		SlowConsumer: time.Duration(opts.SlowConsumerMS) * time.Millisecond,
	})
	if err != nil {
		return stream.Metrics{}, err
	}
	var readers []io.Reader
	for _, tr := range tracks {
		for _, sample := range tr.Samples {
			readers = append(readers, bytes.NewReader(sample.Payload))
		}
	}
	_, err = pipe.Process(ctx, io.MultiReader(readers...), func(context.Context, stream.Block) error {
		return nil
	})
	return pipe.Snapshot(), err
}

func judgeDemo(s manifest.Summary, m stream.Metrics, queue int, budget int64) string {
	ok := s.Segments == 3 && s.Silent == 1 && s.Duplicates == 1 && s.MetadataConflicts == 3
	ok = ok && m.QueuePeak <= int64(queue) && m.BufferPeakBytes <= budget
	ok = ok && m.ResidualTasks == 0 && m.BlocksProduced > 0
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func countKind(xs []model.Exception, kind string) int {
	n := 0
	for _, x := range xs {
		if x.Kind == kind {
			n++
		}
	}
	return n
}
