package report

import (
	"bytes"
	"testing"

	"mediaaudit/internal/model"
	"mediaaudit/internal/render"
	"mediaaudit/internal/timeline"
)

func items() []timeline.Item {
	return []timeline.Item{
		{Seq: 0, Track: model.Track{ID: "v1", Kind: model.KindVideo}, Kind: timeline.ItemVideo,
			Key: "video|v1|0|0", Span: model.Span{Start: model.RatFromInt(0), End: model.RatFromInt(1)},
			Source: "index", SourceIdx: 0, Payload: "f0"},
		{Seq: 1, Track: model.Track{ID: "s1", Kind: model.KindSubtitle}, Kind: timeline.ItemSubtitle,
			Key: "subtitle|s1|1|0|0|1", Span: model.Span{Start: model.RatFromInt(0), End: model.RatFromInt(1)},
			Source: "srt", SourceIdx: 1, Payload: "hi"},
	}
}

func TestItemsDigestStableAndOrdered(t *testing.T) {
	a := ItemsDigest(items())
	b := ItemsDigest(items())
	if a != b || len(a) != 64 {
		t.Fatalf("digest unstable %s %s", a, b)
	}
	rev := items()
	rev[0], rev[1] = rev[1], rev[0]
	if ItemsDigest(rev) == a {
		t.Fatal("order must matter")
	}
	changed := items()
	changed[1].Payload = "hi!"
	if ItemsDigest(changed) == a {
		t.Fatal("payload must be hashed")
	}
}

func TestCanonicalEncodingIsUnambiguous(t *testing.T) {
	// Payloads containing separator lookalikes must not collide.
	x := items()
	y := items()
	x[0].Payload = "a\x1fb"
	y[0].Payload = "a"
	y[0].Source = "b"
	if ItemsDigest(x) == ItemsDigest(y) {
		t.Fatal("length prefixing failed to separate fields")
	}
}

func TestFramesDigestIncludesErrors(t *testing.T) {
	frames := []render.FrameRecord{
		{Point: model.RatFromInt(0), SampleIdx: 0, Keyframe: true, Origin: "index",
			PixelHash: "aa", ByteHash: "bb", Width: 2, Height: 2, PayloadLen: 4},
	}
	base := FramesDigest(frames)
	bad := []render.FrameRecord{frames[0]}
	bad[0].Error = model.NewError(model.CodeSampleRead, "render", "v1", model.InputPos{Offset: 9}, "short")
	if FramesDigest(bad) == base {
		t.Fatal("error records must hash differently")
	}
}

func TestSplit(t *testing.T) {
	chunks := Split(items(), 1)
	if len(chunks) != 2 {
		t.Fatalf("chunks=%d", len(chunks))
	}
	if got := Split(items(), 32); len(got) != 1 {
		t.Fatalf("one chunk expected, got %d", len(got))
	}
	if Split(items(), 0) != nil || Split(nil, 4) != nil {
		t.Fatal("degenerate splits must be empty")
	}
}

func TestChunkFinalizeAndInputDigest(t *testing.T) {
	its := items()
	c := Chunk{Index: 3, Items: its, InputDigest: InputDigest("v1", "s1", "32")}
	c.Finalize()
	if c.FirstSeq != 0 || c.LastSeq != 1 || c.Digest == "" || c.Segment() != "chunk-00003" {
		t.Fatalf("chunk %+v", c)
	}
	same := Chunk{Index: 3, Items: items(), InputDigest: InputDigest("v1", "s1", "32")}
	same.Finalize()
	if same.Digest != c.Digest {
		t.Fatal("identical inputs must produce identical digests")
	}
	different := Chunk{Index: 3, Items: items(), InputDigest: InputDigest("v1", "s1", "64")}
	different.Finalize()
	if different.Digest == c.Digest {
		t.Fatal("different parameters must change the digest")
	}
}

func TestRatio(t *testing.T) {
	if NewRatio(3, 0).String() != "0.000" {
		t.Fatal("zero denominator")
	}
	if NewRatio(2, 2).String() != "1.000" || NewRatio(1, 3).String() != "0.333" {
		t.Fatalf("ratios %s %s", NewRatio(2, 2), NewRatio(1, 3))
	}
	m := Metrics{RoundtripConsistency: NewRatio(4, 4), KeyframeIndexHitRate: NewRatio(4, 4), SegmentHashMatch: NewRatio(2, 2)}
	if !m.AllHeld() {
		t.Fatal("perfect metrics must report held")
	}
	var buf bytes.Buffer
	writeField(&buf, "x")
	if buf.String() != "1:x\x1f" {
		t.Fatalf("field encoding %q", buf.String())
	}
}

func TestMetricsNotHeldWhenInapplicable(t *testing.T) {
	perfect := NewRatio(1, 1)
	m := Metrics{RoundtripConsistency: NewRatio(0, 0), KeyframeIndexHitRate: perfect, SegmentHashMatch: perfect}
	if m.AllHeld() {
		t.Fatal("an inapplicable ratio must not read as a perfect score")
	}
}
