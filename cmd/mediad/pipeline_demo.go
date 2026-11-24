package main

import (
	"context"
	"time"

	"mediad/internal/audio"
	"mediad/internal/stream"
)

// buildAudio runs the full deterministic pipeline on a generated 5.1 PCM
// frame, converting 44.1 kHz -> 48 kHz and normalizing loudness.
func buildAudio(r *rng) (audio.AudioStats, audio.Frame) {
	in := audio.Frame{
		SampleRate: 44100, Channels: audio.Layout51,
		Samples: r.pcm(6 * 128), // 128 complete 5.1 periods
	}
	out, st, err := audio.Process(in, audio.PipelineConfig{
		TargetRate: 48000,
		Loudness:   audio.DefaultLoudness,
	})
	if err != nil {
		panic(err) // fixture is a supported, non-empty layout
	}
	return st, out
}

// runBoundedStream drives a slow consumer through a hard-capped buffer and
// returns its pressure stats. The consumer delay is the CLI-provided one.
func runBoundedStream(ctx context.Context, maxBytes, consumerDelayMS int, r *rng) (stream.Stats, error) {
	// Item cap is derived from the byte cap so both limits are explicit and
	// the peak can never exceed the acceptance ceiling.
	s, err := stream.New(stream.Config{MaxBytes: maxBytes, MaxItems: 64})
	if err != nil {
		return stream.Stats{}, err
	}

	itemSize := maxBytes / 32 // 32 items would saturate the byte cap
	if itemSize <= 0 {
		itemSize = 1
	}

	consumed := make(chan struct{})
	go func() {
		defer close(consumed)
		for {
			item, ok, gerr := s.Get(ctx)
			if gerr != nil || !ok {
				return
			}
			if consumerDelayMS > 0 {
				time.Sleep(time.Duration(consumerDelayMS) * time.Millisecond)
			}
			_ = item
		}
	}()

	// Produce more items than the cap can hold; Put must block under pressure.
	const producers = 40
	for i := 0; i < producers; i++ {
		if err := s.Put(ctx, stream.Item{Size: itemSize, Label: "frame"}); err != nil {
			s.Close()
			<-consumed
			return s.Snapshot(), err
		}
		// Deterministic jitter: every 7th producer adds a tiny pause so the
		// consumer genuinely drains space between writes.
		if i%7 == 0 && consumerDelayMS > 0 {
			time.Sleep(time.Duration(consumerDelayMS) * time.Millisecond / 2)
		}
	}
	s.Close()
	<-consumed
	return s.Snapshot(), nil
}
