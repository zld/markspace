package pipeline

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mediapipe/internal/audio"
	"mediapipe/internal/clock"
	"mediapipe/internal/ingest"
	"mediapipe/internal/manifest"
)

func TestPoolCancelPropagates(t *testing.T) {
	pool, err := NewPool(context.Background(), 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	boom := fmt.Errorf("job failed")
	release := make(chan struct{})
	var started int32
	// First two jobs occupy the workers; one of them fails.
	for i := 0; i < 2; i++ {
		i := i
		if err := pool.Submit(func(ctx context.Context) error {
			atomic.AddInt32(&started, 1)
			if i == 0 {
				return boom
			}
			<-release
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Queue fills; once the failure cancels the pool, Submit must fail and
	// Wait must return the job error with no leftover work.
	var submitErrs int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pool.Submit(func(ctx context.Context) error { return nil }); err != nil {
				atomic.AddInt32(&submitErrs, 1)
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&started) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if err := pool.Wait(); err == nil || err.Error() != "job failed" {
		t.Fatalf("Wait err=%v", err)
	}
	if atomic.LoadInt32(&submitErrs) == 0 {
		t.Fatal("expected some submits to fail after cancel")
	}
}

func TestPoolExternalCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := NewPool(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := pool.Submit(func(context.Context) error { return nil }); err == nil {
		t.Fatal("submit on cancelled pool must fail")
	}
	_ = pool.Wait()
}

func TestPoolWorkerBounds(t *testing.T) {
	if _, err := NewPool(context.Background(), 0, 1); err == nil {
		t.Fatal("workers=0 must fail")
	}
	if _, err := NewPool(context.Background(), 9, 1); err == nil {
		t.Fatal("workers=9 must fail")
	}
}

// testSource serves segments from memory.
type testSource map[string][]byte

func (s testSource) ReadSegment(uri string) ([]byte, error) {
	b, ok := s[uri]
	if !ok {
		return nil, fmt.Errorf("missing %s", uri)
	}
	return b, nil
}

// buildTrack makes a 4-slot playlist of 0.5 s mono segments; slot 1 is a
// manifest gap and slot 2's bytes are truncated.
func buildTrack(trackID string) (*manifest.Playlist, testSource) {
	src := testSource{}
	var refs []manifest.SegmentRef
	dur := clock.FromSeconds(0.5)
	cursor := clock.Ticks(0)
	for i := int64(0); i < 4; i++ {
		iv := clock.Interval{Start: cursor, End: cursor + dur}
		cursor += dur
		if i == 1 {
			refs = append(refs, manifest.SegmentRef{TrackID: trackID, Index: -1, Interval: iv, Missing: true})
			continue
		}
		uri := fmt.Sprintf("%s/seg%d.m4s", trackID, i)
		frames := int(dur)
		pcm := make([]byte, frames*2)
		for f := 0; f < frames; f++ {
			v := int16(3000 * math.Sin(2*math.Pi*440*float64(f)/48000))
			pcm[2*f] = byte(v)
			pcm[2*f+1] = byte(v >> 8)
		}
		seg := ingest.BuildSegment(pcm)
		if i == 2 {
			seg = seg[:len(seg)-500] // truncated -> unrecoverable
		}
		src[uri] = seg
		refs = append(refs, manifest.SegmentRef{TrackID: trackID, Index: i, URI: uri, Interval: iv})
	}
	return &manifest.Playlist{TrackID: trackID, Refs: refs, Complete: true}, src
}

// runTrack executes ingest -> audio for every slot through the pool.
func runTrack(t *testing.T, pl *manifest.Playlist, src testSource, workers int) *TrackMerge {
	t.Helper()
	v := &ingest.Validator{Src: src, Channels: 1, MaxRetries: 2}
	pool, err := NewPool(context.Background(), workers, 4)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var results []SegmentResult
	for _, ref := range pl.Refs {
		ref := ref
		err := pool.Submit(func(ctx context.Context) error {
			rec, err := v.Ingest(ctx, ref)
			if err != nil {
				return err
			}
			res := SegmentResult{Record: rec}
			if rec.Status == ingest.OK || rec.Status == ingest.Recovered {
				samples := audio.S16LEToFloat(rec.Payload)
				rs, _ := audio.NewResampler(clock.Rate, clock.Rate, 1)
				samples = rs.Process(samples)
				samples = append(samples, rs.Flush()...)
				res.Samples = samples
				res.Frames = int64(len(samples))
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.Wait(); err != nil {
		t.Fatal(err)
	}
	tm, err := MergeTrack(pl, results, 1)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestCrossStageInvariants(t *testing.T) {
	pl, src := buildTrack("trk")
	tm := runTrack(t, pl, src, 3)

	// Every manifest slot has exactly one record.
	if len(tm.Records) != len(pl.Refs) {
		t.Fatalf("records=%d refs=%d", len(tm.Records), len(pl.Refs))
	}
	// Faults propagate: slot 1 missing, slot 2 unrecoverable.
	if tm.Records[1].Status != ingest.Missing {
		t.Fatalf("slot1=%v", tm.Records[1].Status)
	}
	if tm.Records[2].Status != ingest.Unrecoverable {
		t.Fatalf("slot2=%v reason=%q", tm.Records[2].Status, tm.Records[2].Reason)
	}
	// Timeline fully preserved: frames == playlist duration, no violations.
	if tm.Frames != int64(pl.Duration()) {
		t.Fatalf("frames=%d want %d", tm.Frames, pl.Duration())
	}
	if len(tm.Violations) != 0 {
		t.Fatalf("violations=%v", tm.Violations)
	}
	// Gap slots are silent.
	gapStart := int64(pl.Refs[1].Interval.Start)
	if tm.Samples[gapStart] != 0 {
		t.Fatal("gap not silent")
	}
}

func TestDeterministicAcrossWorkers(t *testing.T) {
	pl, src := buildTrack("trk")
	a := runTrack(t, pl, src, 1)
	b := runTrack(t, pl, src, 4)
	if AudioHash(a.Samples) != AudioHash(b.Samples) {
		t.Fatal("audio hash differs across worker counts")
	}
	if ManifestHash([]*TrackMerge{a}) != ManifestHash([]*TrackMerge{b}) {
		t.Fatal("manifest hash differs across worker counts")
	}
	// Repeat execution: identical digest again.
	c := runTrack(t, pl, src, 4)
	if AudioHash(a.Samples) != AudioHash(c.Samples) {
		t.Fatal("repeat run hash differs")
	}
}
