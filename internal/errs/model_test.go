package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewAndErrorString(t *testing.T) {
	e := New(KindContainer, "clip.mp4", 128, 3, 2, "truncated box")
	if e.Offset != 128 || e.TrackID != 2 || e.Segment != 3 || e.Kind != KindContainer {
		t.Fatalf("fields lost: %+v", e)
	}
	s := e.Error()
	for _, want := range []string{"container", "clip.mp4", "offset=128", "track=2", "segment=3", "truncated box"} {
		if !contains(s, want) {
			t.Fatalf("Error() = %q, missing %q", s, want)
		}
	}
}

func TestUnknownOffsetOmitted(t *testing.T) {
	e := New(KindManifest, "a.m3u8", Unknown, Unknown, 0, "bad tag")
	s := e.Error()
	if contains(s, "offset=") || contains(s, "segment=") {
		t.Fatalf("unknown locators must be omitted, got %q", s)
	}
}

func TestWrapPreservesFields(t *testing.T) {
	base := New(KindTrack, "x.mp4", 10, Unknown, 1, "bad timescale")
	wrapped := Wrap(base, errors.New("underflow"))
	if wrapped == base {
		t.Fatal("Wrap must return a copy")
	}
	if wrapped.Offset != 10 || wrapped.TrackID != 1 || wrapped.Cause != "underflow" {
		t.Fatalf("fields/cause lost: %+v", wrapped)
	}
}

func TestAsError(t *testing.T) {
	e := New(KindRTP, "rtp.bin", 4, Unknown, 0, "gap")
	var err error = e
	got, ok := AsError(fmt.Errorf("outer: %w", err))
	if !ok || got.Offset != 4 {
		t.Fatalf("AsError failed: %+v ok=%v", got, ok)
	}
	if _, ok := AsError(errors.New("plain")); ok {
		t.Fatal("plain error must not be a structured Error")
	}
}

func TestCollectionOrderingAndFirstOffset(t *testing.T) {
	var c Collection
	c.Add(New(KindContainer, "f.mp4", 200, Unknown, 0, "late"))
	c.Add(New(KindContainer, "f.mp4", 40, Unknown, 0, "early"))
	c.Add(New(KindManifest, "f.mp4", Unknown, Unknown, 0, "no offset"))
	c.Add(nil)
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
	if c.FirstOffset("f.mp4") != 40 {
		t.Fatalf("FirstOffset = %d, want 40", c.FirstOffset("f.mp4"))
	}
	if c.FirstOffset("missing") != Unknown {
		t.Fatal("unknown source must report Unknown")
	}
	if !c.HasKind(KindManifest) || c.HasKind(KindTimeline) {
		t.Fatal("HasKind classification wrong")
	}
	// A plain error gets a generic structured entry instead of panicking.
	c.Add(errors.New("disk gone"))
	if !c.HasKind(KindIO) {
		t.Fatal("plain error should be classified as KindIO")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
