package pipeline

import (
	"context"

	"example.com/media-kernel/codec"
	"example.com/media-kernel/damage"
	"example.com/media-kernel/index"
	"example.com/media-kernel/ingest"
	"example.com/media-kernel/media"
	"example.com/media-kernel/provenance"
	"example.com/media-kernel/sample"
)

// Options controls one verification/orchestration run.
type Options struct {
	Codec          codec.Type
	MaxMemoryBytes int64
	QueueCapacity  int
}

// Result is the complete deterministic analysis for one fixture.
type Result struct {
	InputHash        string
	DuplicatePackets int
	MissingSequences []uint64
	Gaps             []ingest.Gap
	Audio            []codec.Analyzed
	NormalMP4        *index.MP4Report
	CorruptMP4       *index.MP4Report
	Segments         []provenance.SegmentSummary
	Damage           damage.Report
	MaxMemoryBytes   int64
	PeakTrackedBytes int64
}

// AnalyzeFixture runs ingestion, bounded audio analysis, MP4 indexing and
// damage scanning. Segment hashes are deterministic for identical bytes.
func AnalyzeFixture(ctx context.Context, f *sample.Fixture, opts Options) (*Result, error) {
	if opts.Codec == "" {
		opts.Codec = codec.TypeOpus
	}
	if opts.MaxMemoryBytes <= 0 {
		opts.MaxMemoryBytes = 8 << 20
	}
	if opts.QueueCapacity <= 0 {
		opts.QueueCapacity = 4
	}

	collector := ingest.NewCollector(ingest.StreamID(f.Name))
	var parseErrs []*media.Error
	for _, raw := range f.RawRTP {
		got := collector.Ingest(raw)
		if got.ParseError != nil {
			parseErrs = append(parseErrs, got.ParseError)
		}
	}

	audio, peak, err := analyzeAudioBounded(ctx, opts, collector.Ordered())
	if err != nil {
		return nil, err
	}

	normal, err := index.AnalyzeMP4(f.NormalMP4)
	if err != nil {
		return nil, err
	}
	corrupt, err := index.AnalyzeMP4(f.CorruptMP4)
	if err != nil {
		return nil, err
	}
	segments, err := BuildSegments(f.NormalMP4, normal)
	if err != nil {
		return nil, err
	}

	scanner := damage.NewScanner()
	scanner.ScanAudio(audio)
	scanner.ScanTimeline(audio)
	gaps := collector.Gaps()
	for _, gap := range gaps {
		scanner.ScanGap(gap, gapTimeRange(gap, audio))
	}
	scanner.ScanIndex(normal.Index)
	scanner.ScanIndex(corrupt.Index)

	report := scanner.Report()
	report.ErrorCount += len(parseErrs)
	return &Result{
		InputHash:        f.InputHash,
		DuplicatePackets: collector.DuplicateCount(),
		MissingSequences: collector.Missing(),
		Gaps:             gaps,
		Audio:            audio,
		NormalMP4:        normal,
		CorruptMP4:       corrupt,
		Segments:         segments,
		Damage:           report,
		MaxMemoryBytes:   opts.MaxMemoryBytes,
		PeakTrackedBytes: peak,
	}, nil
}

func analyzeAudioBounded(ctx context.Context, opts Options, packets []*ingest.Packet) ([]codec.Analyzed, int64, error) {
	stream := NewStream[*ingest.Packet](Config{
		QueueCapacity:  opts.QueueCapacity,
		MaxMemoryBytes: opts.MaxMemoryBytes,
	})
	results := make([]codec.Analyzed, 0, len(packets))
	done := make(chan struct{})
	var consumerErr error
	go func() {
		defer close(done)
		for env := range stream.Items() {
			p := env.Value
			results = append(results, codec.Analyze(opts.Codec, p.ExtSequence, p.Header.Timestamp, p.Payload))
			env.Done()
		}
	}()

	for _, p := range packets {
		size := int64(len(p.Payload))
		if size == 0 {
			size = 1 // account for a zero-length DTX packet envelope
		}
		if err := stream.Push(ctx, p, size); err != nil {
			consumerErr = err
			break
		}
	}
	stream.Close()
	<-done
	if consumerErr != nil {
		return nil, 0, consumerErr
	}
	_, peak, _ := stream.Stats()
	return results, peak, nil
}

func gapTimeRange(gap ingest.Gap, packets []codec.Analyzed) *media.TimeRange {
	var prevEnd *media.Timestamp
	var nextStart *media.Timestamp
	for i := range packets {
		p := &packets[i]
		if p.Range == nil || p.Error != nil {
			continue
		}
		if p.ExtSequence < gap.First {
			end := p.Range.End
			prevEnd = &end
		}
		if p.ExtSequence > gap.Last && nextStart == nil {
			start := p.Range.Start
			nextStart = &start
		}
	}
	if prevEnd == nil || nextStart == nil {
		return nil
	}
	converted, err := nextStart.Timebase.Convert(nextStart.Ticks, prevEnd.Timebase)
	if err != nil || converted < prevEnd.Ticks {
		return nil
	}
	r, err := media.NewRange(prevEnd.Timebase, prevEnd.Ticks, converted-prevEnd.Ticks)
	if err != nil {
		return nil
	}
	return &r
}
