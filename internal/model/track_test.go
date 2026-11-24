package model

import (
	"encoding/json"
	"testing"
)

func TestHashJSONRoundTrip(t *testing.T) {
	h, err := ParseHash("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(h)
	if err != nil || string(b) != `"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"` {
		t.Fatalf("hash marshal = %s, %v", b, err)
	}
	var back Hash
	if err := json.Unmarshal(b, &back); err != nil || back != h {
		t.Fatalf("hash unmarshal mismatch: %v", err)
	}
	if _, err := ParseHash("nope"); err == nil {
		t.Fatal("expected error on invalid hash")
	}
}

func TestTimelineValidate(t *testing.T) {
	good := &Timeline{Tracks: []*Track{
		{ID: "v1", Kind: KindVideo, FPS: 30, Frames: []Frame{
			{TrackID: "v1", Index: 0, TimeRate: 90000},
		}},
		{ID: "a1", Kind: KindAudio, SampleRate: 48000, Channels: 2, Frames: []Frame{
			{TrackID: "a1", Index: 0, TimeRate: 48000},
		}},
	}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid timeline rejected: %v", err)
	}

	bad := []*Timeline{
		{Tracks: []*Track{{ID: "", Kind: KindVideo, FPS: 30}}},
		{Tracks: []*Track{
			{ID: "x", Kind: KindVideo, FPS: 30},
			{ID: "x", Kind: KindVideo, FPS: 30},
		}},
		{Tracks: []*Track{{ID: "x", Kind: "video?"}}},
		{Tracks: []*Track{{ID: "x", Kind: KindVideo, FPS: 30, Frames: []Frame{
			{TrackID: "x", Index: 2, TimeRate: 90000},
		}}}},
	}
	for i, tl := range bad {
		if err := tl.Validate(); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestTrackByID(t *testing.T) {
	tl := &Timeline{Tracks: []*Track{{ID: "a"}, {ID: "b"}}}
	if tl.TrackByID("b") == nil || tl.TrackByID("missing") != nil {
		t.Fatal("TrackByID lookup wrong")
	}
}
