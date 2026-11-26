package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"mediatrace/internal/audio"
	"mediatrace/internal/cache"
	"mediatrace/internal/ebml"
	"mediatrace/internal/output"
	"mediatrace/internal/timeline"
)

// Result summarizes a completed run for terminal reporting.
type Result struct {
	CacheState   cache.State
	Manifest     *output.Manifest
	OutputPath   string
	MaxTimeError int64
	ByteDrift    int64
	QualityFail  bool
}

// Runner executes one end-to-end processing plan.
type Runner struct {
	FF audio.Runner
}

// NewRunner returns a Runner using the real FFmpeg binary.
func NewRunner() *Runner {
	return &Runner{FF: audio.ExecRunner{}}
}

// Run executes the plan. Input corruption returns an *ebml.Error carrying a
// stable code and byte offset; callers map it to ExitInputCorrupt.
func (r *Runner) Run(ctx context.Context, p Plan) (*Result, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	inputFile, err := os.Open(p.InputPath)
	if err != nil {
		return nil, err
	}
	defer inputFile.Close()
	fi, err := inputFile.Stat()
	if err != nil {
		return nil, err
	}

	key, inputSHA, err := cache.Key(inputFile)
	if err != nil {
		return nil, err
	}
	if _, err := inputFile.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	store, err := cache.NewStore(p.CacheDir)
	if err != nil {
		return nil, err
	}

	idx, cacheState, lookupErr := store.Lookup(key, p.InputPath, inputSHA)
	if lookupErr != nil && !errors.Is(lookupErr, ebml.ErrIndexMismatch) {
		return nil, lookupErr
	}
	if idx != nil {
		idx.Close()
	}

	// Always (re)scan the input: the scan itself validates structural
	// integrity, which a cache hit cannot prove for a truncated file.
	doc, entries, err := r.scanAndIndex(ctx, p, store, key, inputSHA, inputFile, fi.Size())
	if err != nil {
		store.Discard(key)
		return nil, err
	}

	events, err := loadEventsFile(p.EventsPath)
	if err != nil {
		return nil, err
	}
	mappings, blockViews := alignEntries(entries, events, p.MaxErrorNS)

	staging, err := output.NewStaging(filepath.Dir(p.OutputDir), filepath.Base(p.OutputDir))
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			staging.Abort()
		}
	}()

	deliveries, qualityFail, err := r.processTracks(ctx, p, doc, entries, mappings, staging)
	if err != nil {
		return nil, err
	}

	manifest := buildManifest(p, doc, inputSHA, fi.Size(), cacheState, entries,
		blockViews, mappings, deliveries, qualityFail)
	manifest.RTP = buildRTPObservations(events)
	staging.SetManifest(manifest)

	if qualityFail {
		// Quality failure still records the failure inside the manifest, but
		// no output directory is published.
		return &Result{CacheState: cacheState, Manifest: &manifest, QualityFail: true}, nil
	}
	final, err := staging.Publish()
	if err != nil {
		return nil, err
	}
	published = true
	return &Result{
		CacheState:   cacheState,
		Manifest:     &manifest,
		OutputPath:   final,
		MaxTimeError: manifest.MaxErrorNS,
	}, nil
}

func (r *Runner) processTracks(ctx context.Context, p Plan, doc *ebml.Document,
	entries []ebml.IndexEntry, mappings []timeline.Mapping, staging *output.Staging,
) ([]output.TrackDelivery, bool, error) {
	var deliveries []output.TrackDelivery
	qualityFail := false
	for _, track := range doc.Tracks {
		if !track.IsAudio {
			continue // video and other tracks are deliberately not rewritten
		}
		d, ok, err := r.processOneTrack(ctx, p, track, doc, entries, mappings, staging)
		if err != nil {
			return nil, false, err
		}
		deliveries = append(deliveries, d)
		if !ok {
			qualityFail = true
		}
	}
	return deliveries, qualityFail, nil
}

func (r *Runner) processOneTrack(ctx context.Context, p Plan, track ebml.Track,
	doc *ebml.Document, entries []ebml.IndexEntry, mappings []timeline.Mapping,
	staging *output.Staging,
) (output.TrackDelivery, bool, error) {
	trackCtx, cancel := context.WithTimeout(ctx, time.Duration(p.MaxDecodeSeconds)*time.Second)
	defer cancel()

	trackSpec := fmt.Sprintf("0:%d", audioTrackIndex(doc, track.Number))
	pcmPath := filepath.Join(staging.Dir(), fmt.Sprintf("track-%d.pcm", track.Number))
	measured, err := audio.DecodeNormalize(trackCtx, r.FF, p.InputPath, trackSpec, pcmPath)
	if err != nil {
		return output.TrackDelivery{}, false, err
	}

	var candidates []candidate
	for _, br := range p.ladder() {
		outPath := filepath.Join(staging.Dir(), fmt.Sprintf("track-%d-%dk.m4a", track.Number, br))
		if err := audio.EncodeAAC(trackCtx, r.FF, pcmPath, outPath, br, measured); err != nil {
			return output.TrackDelivery{}, false, err
		}
		stats, err := audio.Measure(trackCtx, r.FF, outPath)
		if err != nil {
			return output.TrackDelivery{}, false, err
		}
		size := int64(0)
		if fi, err := os.Stat(outPath); err == nil {
			size = fi.Size()
		}
		candidates = append(candidates, candidate{
			bitrateKbps: br, path: outPath, stats: stats, sizeBytes: size,
		})
	}
	best := chooseSmallestPassing(candidates)
	// PCM intermediates are no longer needed once every candidate is encoded.
	_ = os.Remove(pcmPath)
	if best == nil {
		for _, c := range candidates {
			_ = os.Remove(c.path)
		}
		return output.TrackDelivery{
			TrackNo: track.Number, CodecID: track.CodecID,
			SourceRateHz: track.Rate, SourceChannels: track.Channels,
			GatePassed: false,
		}, false, nil
	}

	info, err := output.InspectMP4(best.path)
	if err != nil {
		return output.TrackDelivery{}, false, err
	}
	drift, err := output.ByteDrift(best.path, info)
	if err != nil {
		return output.TrackDelivery{}, false, err
	}
	sha, size, err := output.FileSHA256(best.path)
	if err != nil {
		return output.TrackDelivery{}, false, err
	}
	finalName := fmt.Sprintf("track-%d.m4a", track.Number)
	if err := os.Rename(best.path, filepath.Join(staging.Dir(), finalName)); err != nil {
		return output.TrackDelivery{}, false, err
	}
	// Remove losing candidates so staging holds only published artifacts.
	for _, c := range candidates {
		if c.path != best.path {
			_ = os.Remove(c.path)
		}
	}
	return output.TrackDelivery{
		TrackNo:        track.Number,
		CodecID:        track.CodecID,
		SourceRateHz:   track.Rate,
		SourceChannels: track.Channels,
		OutputFile:     finalName,
		OutputSHA256:   sha,
		OutputBytes:    size,
		BitrateKbps:    best.bitrateKbps,
		SampleCount:    info.SampleCount,
		Timescale:      info.Timescale,
		IntegratedLUFS: best.stats.Integrated,
		TruePeakDBTP:   best.stats.TruePeak,
		GatePassed:     true,
		ByteDrift:      drift,
	}, true, nil
}
