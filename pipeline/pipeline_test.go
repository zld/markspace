package pipeline

import (
	"bytes"
	"encoding/json"
	"testing"

	"mediacore/index"
	"mediacore/media"
)

func volume(v float64) *float64 { return &v }

func buildStream(t *testing.T, frames []*media.Frame) ([]byte, *index.Index) {
	t.Helper()
	data, err := media.EncodeStream(frames)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return data, idx
}

// timeline builds n frames of 40 ms each. quiet[i] gives the dBFS metadata for
// frame i when non-nil; a nil entry produces a frame without metadata.
func timeline(n int, perFrame func(i int) *float64) []*media.Frame {
	frames := make([]*media.Frame, n)
	for i := 0; i < n; i++ {
		f := &media.Frame{
			Seq:           uint32(i),
			RTPTimestamp:  uint32(i * 3600),
			CaptureTimeMS: int64(i) * 40,
			Width:         8,
			Height:        8,
			DurationTicks: 3600,
			StreamID:      1,
			Payload:       make([]byte, 64),
		}
		for j := range f.Payload {
			f.Payload[j] = byte(i*31 + j*7)
		}
		if perFrame != nil {
			if v := perFrame(i); v != nil {
				b, _ := json.Marshal(media.Meta{VolumeDBFS: v})
				f.MetaRaw = b
			}
		}
		frames[i] = f
	}
	return frames
}

func TestSegmentsSplitOnMaximumDuration(t *testing.T) {
	data, idx := buildStream(t, timeline(200, nil))
	src := NewBufferSource(data, media.DefaultLimits())
	cfg := DefaultConfig(media.DefaultTimescale)
	res, err := Run(idx, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 200 frames * 40 ms = 8 s; 4 s max => 2 segments.
	if len(res.Segments) != 2 {
		t.Fatalf("segments=%d want 2", len(res.Segments))
	}
	for _, s := range res.Segments[:1] {
		if s.ClosedBy != "duration" {
			t.Fatalf("first segment closed_by=%s", s.ClosedBy)
		}
	}
	if res.Segments[len(res.Segments)-1].ClosedBy != "end" {
		t.Fatalf("last segment closed_by=%s", res.Segments[len(res.Segments)-1].ClosedBy)
	}
	if res.FrameCount != 200 {
		t.Fatalf("frame count=%d", res.FrameCount)
	}
}

func TestSegmentsSplitOnBurstLossGap(t *testing.T) {
	frames := timeline(100, nil)
	// Drop a burst of 20 frames (800 ms) in the middle.
	kept := append([]*media.Frame{}, frames[:40]...)
	kept = append(kept, frames[60:]...)
	data, idx := buildStream(t, kept)
	res, err := Run(idx, NewBufferSource(data, media.DefaultLimits()), DefaultConfig(media.DefaultTimescale))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) < 2 {
		t.Fatalf("burst loss must cut a boundary, got %d segments", len(res.Segments))
	}
	sawGap := false
	for _, s := range res.Segments {
		if s.ClosedBy == "gap" {
			sawGap = true
			if s.MissingFrames < 19 {
				t.Fatalf("segment missing=%d", s.MissingFrames)
			}
		}
	}
	if !sawGap {
		t.Fatalf("no gap-closed segment: %+v", res.Segments)
	}
	if len(res.Gaps) == 0 {
		t.Fatal("expected gap refs")
	}
}

func TestSegmentsRespectBoundaryTolerance(t *testing.T) {
	cfg := DefaultSegmentConfig(media.DefaultTimescale)
	seg, _ := NewSegmenter(cfg)
	frames := timeline(101, nil) // 4040 ms total: last frame is 40ms over 4s
	data, idx := buildStream(t, frames)
	_ = data
	first, _ := seg.Segments(idx.Entries)
	if first[0].FrameCount < 100 {
		t.Fatalf("tolerance should keep the near-boundary frame, got %d", first[0].FrameCount)
	}
}

func TestSilenceDetectedFromMetadata(t *testing.T) {
	// frames 20..39 quiet at -60 dBFS: 20 * 40 ms = 800 ms.
	frames := timeline(60, func(i int) *float64 {
		if i >= 20 && i < 40 {
			return volume(-60)
		}
		return volume(-12)
	})
	data, idx := buildStream(t, frames)
	res, err := Run(idx, NewBufferSource(data, media.DefaultLimits()), DefaultConfig(media.DefaultTimescale))
	if err != nil {
		t.Fatal(err)
	}
	confirmed := Confirmed(res.Silence)
	if len(confirmed) != 1 {
		t.Fatalf("confirmed quiet intervals=%d %+v", len(confirmed), res.Silence)
	}
	if got := confirmed[0].DurationMS(); got < 760 || got > 840 {
		t.Fatalf("silence duration=%dms want ~800", got)
	}
	if res.SilenceConfirmedMS != confirmed[0].DurationMS() {
		t.Fatalf("aggregate=%d", res.SilenceConfirmedMS)
	}
}

func TestSilenceMissingMetadataIsUnknownNotSilent(t *testing.T) {
	// Frames 10..30 have no metadata at all.
	frames := timeline(50, func(i int) *float64 {
		if i >= 10 && i < 30 {
			return nil
		}
		return volume(-80) // everything else is very quiet
	})
	data, idx := buildStream(t, frames)
	res, err := Run(idx, NewBufferSource(data, media.DefaultLimits()), DefaultConfig(media.DefaultTimescale))
	if err != nil {
		t.Fatal(err)
	}
	// No confirmed silence may span the metadata-free frames.
	unknownStart := idx.Entries[10].Ticks
	unknownEnd := idx.Entries[30].Ticks
	for _, iv := range Confirmed(res.Silence) {
		if iv.StartTicks < unknownEnd && iv.EndTicks > unknownStart {
			t.Fatalf("missing metadata was reported as silence: %+v", iv)
		}
	}
	unknown := 0
	for _, iv := range res.Silence {
		if iv.State == SilenceUnknown {
			unknown++
			if iv.StartTicks > unknownStart || iv.EndTicks < unknownEnd {
				t.Fatalf("unknown interval %+v does not cover the metadata-free frames", iv)
			}
		}
	}
	if unknown == 0 {
		t.Fatalf("expected unknown interval, got %+v", res.Silence)
	}
	if res.SilenceUnknownMS == 0 {
		t.Fatal("unknown milliseconds not aggregated")
	}
}

func TestSilenceCorruptMetadataIsUnknown(t *testing.T) {
	frames := timeline(30, func(i int) *float64 { return volume(-90) })
	// Corrupt one metadata block but keep the record digest valid.
	frames[10].MetaRaw = []byte("{not json")
	data, _ := media.EncodeStream(frames)
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if idx.Entries[10].MetaStatus != string(media.MetaCorrupt) {
		t.Fatalf("meta status=%s", idx.Entries[10].MetaStatus)
	}
	res, err := Run(idx, NewBufferSource(data, media.DefaultLimits()), DefaultConfig(media.DefaultTimescale))
	if err != nil {
		t.Fatal(err)
	}
	for _, iv := range res.Silence {
		if iv.State == SilenceConfirmed && iv.StartTicks <= idx.Entries[10].Ticks && iv.EndTicks > idx.Entries[10].Ticks {
			t.Fatalf("corrupt metadata frame was swallowed by a confirmed silence: %+v", iv)
		}
	}
}

func TestFingerprintDuplicatesAcrossTime(t *testing.T) {
	frames := timeline(30, nil)
	// Make frames 0, 10 and 20 share identical content.
	copy(frames[10].Payload, frames[0].Payload)
	copy(frames[20].Payload, frames[0].Payload)
	data, idx := buildStream(t, frames)
	res, err := Run(idx, NewBufferSource(data, media.DefaultLimits()), DefaultConfig(media.DefaultTimescale))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DuplicateGroups) != 1 {
		t.Fatalf("duplicate groups=%d want 1 (%+v)", len(res.DuplicateGroups), res.DuplicateGroups)
	}
	g := res.DuplicateGroups[0]
	if g.Count != 3 || g.FirstSeq != 0 {
		t.Fatalf("group=%+v", g)
	}
	if res.DuplicateFrames != 2 {
		t.Fatalf("duplicate frames=%d want 2", res.DuplicateFrames)
	}
	// Duplicates remain in the frame and segment counts.
	if res.FrameCount != 30 || res.Segments[0].FrameCount != 30 {
		t.Fatalf("dedupe must not delete frames: frames=%d seg=%d", res.FrameCount, res.Segments[0].FrameCount)
	}
}

func TestPipelineDeterministic(t *testing.T) {
	frames := timeline(120, func(i int) *float64 {
		if i%30 < 5 {
			return volume(-70)
		}
		return volume(-10)
	})
	copy(frames[60].Payload, frames[3].Payload)
	data, idx := buildStream(t, frames)
	cfg := DefaultConfig(media.DefaultTimescale)
	runOnce := func() *Result {
		res, err := Run(idx, NewBufferSource(data, media.DefaultLimits()), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	a, b := runOnce(), runOnce()
	if len(a.Segments) != len(b.Segments) {
		t.Fatal("segment count differs between runs")
	}
	for i := range a.Segments {
		if a.Segments[i] != b.Segments[i] {
			t.Fatalf("segment %d differs: %+v vs %+v", i, a.Segments[i], b.Segments[i])
		}
	}
	if len(a.DuplicateGroups) != len(b.DuplicateGroups) {
		t.Fatal("duplicate groups differ")
	}
	for i := range a.DuplicateGroups {
		if a.DuplicateGroups[i].Fingerprint != b.DuplicateGroups[i].Fingerprint {
			t.Fatal("fingerprints differ between runs")
		}
	}
}

func TestBlockNormalizeIgnoresSmallNoise(t *testing.T) {
	cfg := FingerprintConfig{Mode: NormalizeBlock, Grid: 4, Quantum: 4}
	f1 := &media.Frame{Width: 8, Height: 8, Payload: make([]byte, 64)}
	f2 := &media.Frame{Width: 8, Height: 8, Payload: make([]byte, 64)}
	for i := range f1.Payload {
		f1.Payload[i] = 128
		f2.Payload[i] = 131 // within the 4-bit quantum
	}
	if cfg.Compute(f1).Hex != cfg.Compute(f2).Hex {
		t.Fatal("block normalization should ignore sub-quantum noise")
	}
	f3 := &media.Frame{Width: 8, Height: 8, Payload: make([]byte, 64)}
	for i := range f3.Payload {
		f3.Payload[i] = 32
	}
	if cfg.Compute(f1).Hex == cfg.Compute(f3).Hex {
		t.Fatal("distinct content must not share a fingerprint")
	}
}

func TestFingerprintRejectsIndexDimensionMismatch(t *testing.T) {
	d, err := NewDeduper(DefaultFingerprintConfig())
	if err != nil {
		t.Fatal(err)
	}
	e := index.Entry{Width: 8, Height: 8, Seq: 1}
	f := &media.Frame{Width: 4, Height: 4, Payload: make([]byte, 16)}
	if _, _, err := d.Add(e, f); err == nil {
		t.Fatal("expected dimension mismatch error")
	} else if media.CategoryOf(err) != media.CatIndexInput {
		t.Fatalf("category=%s", media.CategoryOf(err))
	}
}

func TestBufferSourceRejectsUnindexedChange(t *testing.T) {
	data, idx := buildStream(t, timeline(5, nil))
	// Change a frame but keep the old index.
	bad := append([]byte(nil), data...)
	bad[media.FrameHeaderSize+2] ^= 0xFF
	src := NewBufferSource(bad, media.DefaultLimits())
	if _, err := Run(idx, src, DefaultConfig(media.DefaultTimescale)); err == nil {
		t.Fatal("pipeline must re-validate records and reject changed bytes")
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	cfg := DefaultConfig(media.DefaultTimescale)
	cfg.Silence.Timescale = 1000
	if err := cfg.Validate(); err == nil {
		t.Fatal("mismatched timescales must be rejected")
	}
}
