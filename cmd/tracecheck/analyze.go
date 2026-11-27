package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/manifest"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/local/tracecheck/internal/pipeline"
)

const maxItemBytes = 4 << 20 // single-block ceiling: 4 MiB

// cmdAnalyze runs the full pipeline over one input file and prints the
// acceptance metrics plus anomaly intervals.
func cmdAnalyze(o options) int {
	bitrates, err := parseBitrates(o.bitratesCSV)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return exitInput
	}
	raw, err := os.ReadFile(o.input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read input: %v\n", err)
		return exitInput
	}
	inputID := pipeline.InputDigest(raw)

	doc, err := mkv.Parse(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return reportCorrupt(err)
	}
	if len(bitrates) > len(doc.Tracks) {
		fmt.Fprintf(os.Stderr,
			"argument error: %d bitrates requested but only %d tracks present\n",
			len(bitrates), len(doc.Tracks))
		return exitInput
	}

	layers := make([]pipeline.LayerSpec, len(bitrates))
	for i, br := range bitrates {
		layers[i] = pipeline.LayerSpec{Bitrate: br, TrackNumber: doc.Tracks[i].Number}
	}
	cacheDir, resumeDir, err := dirs(o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "directory error: %v\n", err)
		return exitInput
	}

	cfg := pipeline.Config{
		InputID:        inputID,
		SegmentNS:      int64(o.segmentMS) * 1_000_000,
		MaxSkewNS:      clock.Tick(o.maxSkewMS) * 1_000_000,
		QueueCapacity:  o.queue,
		MaxItemBytes:   maxItemBytes,
		SlowConsumerMS: o.slowConsumerMS,
		Layers:         layers,
		CacheDir:       cacheDir,
		ResumeDir:      resumeDir,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rep, err := pipeline.Run(ctx, doc, cfg)
	if err != nil {
		if te := clock.AsTraceError(err); te != nil && te.Code == clock.CodeCancelled {
			fmt.Fprintf(os.Stderr, "cancelled: %v\n", err)
			return exitBusiness
		}
		fmt.Fprintf(os.Stderr, "pipeline error: %v\n", err)
		return exitInput
	}

	m := rep.Manifest
	printMetric("segment_count", m.SegmentCount)
	printMetric("aligned_count", m.AlignedCount)
	printMetric("max_skew_ns", m.MaxSkewNS)
	printMetric("packet_loss", m.PacketLoss)
	printMetric("queue_peak", m.QueuePeak)
	printMetric("cache_hits", m.CacheHits)
	printMetric("manifest_sha256", m.ManifestSHA256)

	printAnomalies(m)
	printFailedRanges(m)
	printMismatches(rep)

	// Business verdict.
	businessFail := false
	if m.AlignedCount != m.SegmentCount {
		businessFail = true
	}
	if m.MaxSkewNS > int64(cfg.MaxSkewNS) {
		businessFail = true
	}
	if m.QueuePeak > o.queue {
		businessFail = true
	}
	if businessFail {
		fmt.Println("verdict=BUSINESS_THRESHOLD_FAILED")
		return exitBusiness
	}
	fmt.Println("verdict=OK")
	return exitOK
}

func reportCorrupt(err error) int {
	te := clock.AsTraceError(err)
	if te != nil {
		fmt.Fprintf(os.Stderr, "corrupt input: code=%s offset=%d path=%s detail=%s\n",
			te.Code, te.Offset, te.Path, te.Msg)
	} else {
		fmt.Fprintf(os.Stderr, "corrupt input: %v\n", err)
	}
	return exitInput
}

func printMetric(name string, value any) {
	fmt.Printf("%s=%v\n", name, value)
}

func printAnomalies(m *manifest.Manifest) {
	for _, a := range m.Anomalies {
		fmt.Printf("anomaly: code=%s track=%d raw_timecode=%d abs_time_ns=%d offset=%d\n",
			a.Code, a.TrackNumber, a.RawTimecode, a.AbsTimeNS, a.SourceOffset)
	}
}

func printFailedRanges(m *manifest.Manifest) {
	for _, r := range m.FailedRanges {
		fmt.Printf("failed_segment_range: start_ns=%d end_ns=%d\n", r[0], r[1])
	}
}

func printMismatches(rep *pipeline.Report) {
	for _, mm := range rep.Mismatches {
		fmt.Fprintf(os.Stderr, "cache mismatch: key=%s reason=%s detail=%s\n",
			mm.Key, mm.Reason, mm.Detail)
	}
}
