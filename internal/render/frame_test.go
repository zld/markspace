package render

import (
	"bytes"
	"image/png"
	"testing"

	"media.rebuild/internal/model"
)

func spec() Spec {
	return Spec{
		Index: 3, TimeNs: 1_200_000_000, SpanStartNs: 1_000_000_000, SpanEndNs: 2_000_000_000,
		SourceEvent: "evt-3",
		Subtitle:    "the picture froze",
		Lanes: []Lane{
			{Track: "video", Kind: "media", Active: true, Items: 1, SkewNs: 12_000_000},
			{Track: "client", Kind: "event", Active: true, Items: 1},
			{Track: "subs", Kind: "subtitle", Active: true, SkewNs: 90_000_000, Over: true},
		},
	}
}

func TestSameSpecSameHash(t *testing.T) {
	a, err := RenderFrame(spec(), "frame-003.png")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RenderFrame(spec(), "frame-003.png")
	if a.SHA256 != b.SHA256 || !bytes.Equal(a.Data, b.Data) {
		t.Fatal("render is not deterministic")
	}
	if len(a.SHA256) != 64 {
		t.Fatalf("hash %q", a.SHA256)
	}
}

func TestFrameDiffersWhenTimeOrStateChanges(t *testing.T) {
	base, _ := RenderFrame(spec(), "f.png")
	other := spec()
	other.TimeNs += 10_000_000
	moved, _ := RenderFrame(other, "f.png")
	if moved.SHA256 == base.SHA256 {
		t.Fatal("moving the marker did not change the frame")
	}
	other = spec()
	other.Subtitle = "the picture resumed"
	sub, _ := RenderFrame(other, "f.png")
	if sub.SHA256 == base.SHA256 {
		t.Fatal("changing the caption did not change the frame")
	}
	other = spec()
	other.Lanes[0].Over = true
	over, _ := RenderFrame(other, "f.png")
	if over.SHA256 == base.SHA256 {
		t.Fatal("changing an over-budget lane did not change the frame")
	}
}

func TestLaneOrderDoesNotAffectHash(t *testing.T) {
	a, _ := RenderFrame(spec(), "f.png")
	s := spec()
	s.Lanes[0], s.Lanes[2] = s.Lanes[2], s.Lanes[0]
	b, _ := RenderFrame(s, "f.png")
	if a.SHA256 != b.SHA256 {
		t.Fatal("lane input order changed the frame")
	}
}

func TestFrameIsValidPNG(t *testing.T) {
	f, err := RenderFrame(spec(), "f.png")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(f.Data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != DefaultWidth || b.Dy() != DefaultHeight {
		t.Fatalf("bounds %v", b)
	}
	if f.Bytes != len(f.Data) {
		t.Fatalf("byte count %d vs %d", f.Bytes, len(f.Data))
	}
}

func TestRejectsBadSpec(t *testing.T) {
	for _, s := range []Spec{
		{Index: -1},
		{TimeNs: -1},
		{Width: 8, Height: 180},
		{Width: 9000, Height: 180},
	} {
		if _, err := Render(s); !model.IsCode(err, model.CodeInvalidArgument) {
			t.Errorf("spec %+v accepted (%v)", s, err)
		}
	}
}

func TestDigestChangesWithOrder(t *testing.T) {
	a := Frame{Index: 0, File: "a.png", SHA256: "aa"}
	b := Frame{Index: 1, File: "b.png", SHA256: "bb"}
	if Digest([]Frame{a, b}) == Digest([]Frame{b, a}) {
		t.Fatal("digest ignored ordering")
	}
	if Digest([]Frame{a, b}) != Digest([]Frame{a, b}) {
		t.Fatal("digest unstable")
	}
}
