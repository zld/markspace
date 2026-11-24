package selftest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mediacheck/internal/adapter"
	mp4a "mediacheck/internal/adapter/mp4"
	"mediacheck/internal/fault"
	"mediacheck/internal/manifest"
	"mediacheck/internal/remux"
	"mediacheck/internal/stats"
	"mediacheck/internal/stream"
	"mediacheck/internal/syncer"
)

// Config carries the runtime parameters of one processing task.
type Config struct {
	JitterMs        int
	MaxBufferBytes  int64
	TargetKbps      float64
	MaxBitrateDelta float64
	AVThresholdNs   int64
	SegmentNs       int64
	ConsumerDelay   time.Duration // simulates a slow consumer when > 0
}

// Result is the outcome of processing one input sample.
type Result struct {
	Name          string
	OK            bool
	Skipped       bool // input could not be processed at all
	Remuxed       bool
	Faults        *fault.List
	DurationNs    int64
	Segments      []syncer.Segment
	Manifest      *manifest.Manifest
	ManifestErr   int64
	ManifestBound int64
	Bitrate       stats.Bitrate
	PeakBuffer    int64
	OutPath       string
}

// DefaultConfig derives a config from the command-line parameters.
func DefaultConfig(jitterMs int, maxBufferBytes int64, targetKbps, maxDelta float64) Config {
	return Config{
		JitterMs:        jitterMs,
		MaxBufferBytes:  maxBufferBytes,
		TargetKbps:      targetKbps,
		MaxBitrateDelta: maxDelta,
		AVThresholdNs:   40_000_000,    // 40 ms
		SegmentNs:       1_000_000_000, // 1 s
	}
}

// streamRemux pushes sample payloads through the bounded queue into the
// remuxer. It returns bytes written and the queue peak. On cancellation or
// error the partial output is removed.
func streamRemux(ctx context.Context, cfg Config, specs []remux.TrackSpec,
	durationNs int64, src func(ti, si int) ([]byte, error), outPath string) (int64, int64, error) {

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return 0, 0, err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return 0, 0, err
	}
	q := stream.New(cfg.MaxBufferBytes)
	w := remux.New(f)
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() { // producer: read payloads, push into the bounded queue
		defer wg.Done()
		defer q.Close()
		for ti := range specs {
			for si := range specs[ti].Samples {
				data, err := src(ti, si)
				if err != nil {
					errCh <- err
					return
				}
				if err := q.Push(ctx, stream.Chunk{Data: data, TrackID: ti}); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	go func() { // consumer: pop chunks and write them out
		defer wg.Done()
		if err := w.WriteHeader(specs, durationNs); err != nil {
			errCh <- err
			return
		}
		for {
			c, err := q.Pop(ctx)
			if err == stream.ErrClosed {
				break
			}
			if err != nil {
				errCh <- err
				return
			}
			if cfg.ConsumerDelay > 0 {
				select {
				case <-time.After(cfg.ConsumerDelay):
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				}
			}
			if err := w.WriteSample(c.TrackID, c.Data); err != nil {
				errCh <- err
				return
			}
		}
		if _, err := w.Finalize(); err != nil {
			errCh <- err
		}
	}()

	wg.Wait()
	peak := q.Peak()
	select {
	case err := <-errCh:
		f.Close()
		os.Remove(outPath)
		return 0, peak, err
	default:
	}
	if err := f.Close(); err != nil {
		os.Remove(outPath)
		return 0, peak, err
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return 0, peak, err
	}
	return st.Size(), peak, nil
}

// finalizeChecks runs the bitrate and manifest boundary gates shared by
// both input paths.
func finalizeChecks(res *Result, cfg Config, fl *fault.List, bytesWritten int64, frameNs int64) {
	res.Bitrate = stats.ComputeBitrate(bytesWritten, res.DurationNs, cfg.TargetKbps)
	if !res.Bitrate.Within(cfg.MaxBitrateDelta) {
		fl.Add(fault.ErrBitrateDelta, -1,
			"actual %.1f kbps deviates %.2f%% from target %.0f kbps (limit %.2f%%)",
			res.Bitrate.ActualKbps, res.Bitrate.Delta*100, cfg.TargetKbps, cfg.MaxBitrateDelta*100)
		return
	}
	bound := frameNs
	if bound < manifest.BoundaryToleranceNanos {
		bound = manifest.BoundaryToleranceNanos
	}
	res.ManifestBound = bound
	res.ManifestErr = res.Manifest.BoundaryError(res.DurationNs)
	if err := res.Manifest.Validate(res.DurationNs, frameNs); err != nil {
		fl.Add(fault.ErrManifestDrift, -1, "%s", err)
		return
	}
	res.OK = true
}

// ProcessMP4 runs the container path: parse, align, remux, verify.
func ProcessMP4(ctx context.Context, cfg Config, reg *adapter.Registry,
	name, path, outDir string) *Result {

	fl := fault.NewList()
	res := &Result{Name: name, Faults: fl}

	hdr := make([]byte, 12)
	if f, err := os.Open(path); err != nil {
		fl.Add(fault.ErrTruncated, 0, "cannot open: %v", err)
		res.Skipped = true
		return res
	} else {
		n, _ := f.Read(hdr)
		f.Close()
		if n < 12 || string(hdr[4:8]) != "ftyp" {
			fl.Add(fault.ErrUnknownMajor, 0, "not an MP4/MOV container")
			res.Skipped = true
			return res
		}
	}
	var brand [4]byte
	copy(brand[:], hdr[8:12])
	major, ok := mp4a.MajorForBrand(brand)
	if !ok {
		fl.Add(fault.ErrUnknownMajor, 8, "unsupported major brand %q", brand)
		res.Skipped = true
		return res
	}
	media, err := reg.Parse(ctx, major, path, fl, 8)
	if err != nil {
		res.Skipped = true // fault (with offset) already recorded
		return res
	}
	defer media.Close()

	res.DurationNs = media.DurationNanos
	v := media.FirstTrack(adapter.KindVideo)
	a := media.FirstTrack(adapter.KindAudio)
	res.Segments = syncer.Align(v, a, cfg.SegmentNs, cfg.AVThresholdNs, fl)
	res.Manifest = buildManifest(name, res.DurationNs, cfg.SegmentNs, res.Segments, nil)

	specs := make([]remux.TrackSpec, 0, len(media.Tracks))
	for _, t := range media.Tracks {
		specs = append(specs, remux.TrackSpec{
			ID: t.ID, Kind: t.Kind, Timescale: t.Timescale, Samples: t.Samples,
		})
	}
	src := func(ti, si int) ([]byte, error) {
		s := media.Tracks[ti].Samples[si]
		buf := make([]byte, s.Size)
		_, err := media.Source.ReadAt(buf, int64(s.Offset))
		return buf, err
	}
	outPath := filepath.Join(outDir, name+".out.mp4")
	written, peak, err := streamRemux(ctx, cfg, specs, res.DurationNs, src, outPath)
	res.PeakBuffer = peak
	if err != nil {
		fl.Add(fault.ErrCanceled, -1, "remux: %v", err)
		return res
	}
	res.Remuxed = true
	res.OutPath = outPath

	frameNs := int64(10_000_000)
	if v != nil && len(v.Samples) > 0 {
		frameNs = v.Timescale.ToNanos(v.Samples[0].Duration)
	}
	finalizeChecks(res, cfg, fl, written, frameNs)
	return res
}

// buildManifest creates one entry per segment window and overlays sync and
// recovery information.
func buildManifest(name string, durationNs, segmentNs int64,
	segs []syncer.Segment, ranges []rangeOverlay) *manifest.Manifest {

	m := &manifest.Manifest{Source: name}
	if segmentNs <= 0 {
		segmentNs = 1_000_000_000
	}
	byIndex := make(map[int]syncer.Segment, len(segs))
	for _, s := range segs {
		byIndex[s.Index] = s
	}
	for start, idx := int64(0), 0; start < durationNs; start, idx = start+segmentNs, idx+1 {
		end := start + segmentNs
		if end > durationNs {
			end = durationNs
		}
		e := manifest.Entry{StartNanos: start, EndNanos: end, Status: manifest.StatusOK}
		if s, ok := byIndex[idx]; ok {
			e.AVDeltaNanos = s.AVDeltaNanos
			if s.Desync {
				e.Status = manifest.StatusDesync
				e.Errors = append(e.Errors, fault.ErrDesync)
			}
		}
		for _, r := range ranges {
			if r.EndNanos <= start || r.StartNanos >= end {
				continue
			}
			if r.Recoverable {
				if e.Status == manifest.StatusOK {
					e.Status = manifest.StatusRecovered
				}
				e.Errors = append(e.Errors, fault.ErrPacketLoss)
			} else {
				e.Status = manifest.StatusUnrecovered
				e.Errors = append(e.Errors, fault.ErrPacketLoss, fault.ErrUnrecoverable)
			}
		}
		m.Add(e)
	}
	return m
}

type rangeOverlay struct {
	StartNanos  int64
	EndNanos    int64
	Recoverable bool
}
