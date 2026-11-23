package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"mediqc/internal/cache"
	"mediqc/internal/checkpoint"
	"mediqc/internal/clock"
	"mediqc/internal/fault"
	"mediqc/internal/manifest"
	"mediqc/internal/stream"
)

// segmentPayload derives deterministic chunk bytes for one work item.
func segmentPayload(seed int64, itemIdx int) []byte {
	buf := make([]byte, chunkSize)
	for off, ctr := 0, uint64(0); off < chunkSize; off, ctr = off+32, ctr+1 {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%d/%d", seed, itemIdx, ctr)))
		copy(buf[off:], sum[:])
	}
	return buf
}

// decodeSink validates RTP packets before handing payloads to the sink.
type decodeSink struct {
	codec *stream.RTPCodec
	inner stream.Sink
}

func (d decodeSink) Write(c stream.Chunk) error {
	payload, err := d.codec.Decode(c.Payload)
	if err != nil {
		return err
	}
	return d.inner.Write(stream.Chunk{Seq: c.Seq, Payload: payload})
}

// runSegments processes the work list through the bounded pipeline,
// committing one checkpoint per segment. cancelAfter >= 0 simulates an
// interruption after that many new commits. It returns the manifest
// built from the checkpoint's committed digests and the buffer
// high-water mark.
func runSegments(ctx context.Context, cfg demoConfig, items []workItem,
	cpDir string, cacheStore *cache.Store, params, origin string,
	cancelAfter int) (*manifest.Manifest, int64, error) {

	cpStore, err := checkpoint.Open(cpDir)
	if err != nil {
		return nil, 0, err
	}
	state, err := cpStore.Load()
	if err != nil {
		return nil, 0, err
	}
	if err := cpStore.SetStage(state, "segment"); err != nil {
		return nil, 0, err
	}

	p := stream.New(cfg.maxBufferByte)
	codec := stream.NewRTPCodec(0x15590)
	sink := decodeSink{codec: codec, inner: &stream.SlowSink{
		Delay: time.Duration(cfg.slowSinkMs) * time.Millisecond}}
	drainDone := make(chan error, 1)
	go func() { drainDone <- stream.Drain(ctx, p, sink) }()

	fail := func(err error) (*manifest.Manifest, int64, error) {
		p.Close(err)
		<-drainDone
		return nil, p.PeakBytes, err
	}

	newCommits := 0
	for i := range items {
		if i < state.Next {
			continue // already committed; never re-commit
		}
		payload := segmentPayload(cfg.seed, i)
		key := cache.Key(payload, items[i].rep.Rate.String(), params)
		var digest string
		got, hit, tampered, err := cacheStore.Get(key, origin)
		if err != nil {
			return fail(err)
		}
		if tampered {
			return fail(cache.ErrTamper)
		}
		if hit {
			digest = string(got)
		} else {
			sum := sha256.Sum256(payload)
			digest = hex.EncodeToString(sum[:])
			if err := cacheStore.Put(key, origin, []byte(digest)); err != nil {
				return fail(err)
			}
		}
		raw, err := codec.Encode(payload)
		if err != nil {
			return fail(err)
		}
		if err := p.Push(ctx, stream.Chunk{Seq: i, Payload: raw}); err != nil {
			return fail(err)
		}
		// Persist the segment digest first; only then advance the checkpoint.
		if err := cpStore.Commit(state, digest, []byte(digest)); err != nil {
			return fail(err)
		}
		newCommits++
		if cancelAfter >= 0 && newCommits >= cancelAfter {
			return fail(fault.New(fault.CodeCancelled, 0, -1,
				fmt.Sprintf("interrupted after %d commits", newCommits),
				"resume from the checkpoint"))
		}
	}
	p.Close(nil)
	if err := <-drainDone; err != nil {
		return nil, p.PeakBytes, err
	}

	m := &manifest.Manifest{Seed: cfg.seed, Timebase: "90000", Params: params}
	for i, it := range items {
		tc, err := clock.FramesToTimecode(it.seg.StartFrame, it.rep.Rate, it.rep.Drop)
		if err != nil {
			return nil, p.PeakBytes, err
		}
		m.Segments = append(m.Segments, manifest.SegmentEntry{
			Index:   i,
			Rep:     it.rep.Name,
			StartTC: tc.String(),
			Frames:  it.seg.Frames(),
			SHA256:  state.Committed[i],
			Source:  string(it.src),
		})
	}
	return m, p.PeakBytes, nil
}
