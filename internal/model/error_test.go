package model

import (
	"errors"
	"strings"
	"testing"
)

func TestMediaErrorLocation(t *testing.T) {
	base := New(ErrInvalidCrop, "crop %d exceeds width %d", 1920, 1280)
	located := base.AtTrack(3).AtOffset(4096)
	if located.Track != 3 || located.Offset != 4096 {
		t.Fatalf("location lost: %+v", located)
	}
	// The original error must stay immutable.
	if base.Track != 0 || base.Offset != -1 {
		t.Fatalf("base error mutated: %+v", base)
	}
	if !strings.Contains(located.Error(), "track=3") || !strings.Contains(located.Error(), "offset=4096") {
		t.Fatalf("error text missing location: %s", located.Error())
	}
}

func TestHasCodeAndWrap(t *testing.T) {
	inner := errors.New("io: short read")
	me := New(ErrMetaUnparseable, "bad KLV").Wrap(inner)
	if !HasCode(me, ErrMetaUnparseable) {
		t.Fatal("HasCode failed on wrapped error")
	}
	if !errors.Is(me, inner) {
		t.Fatal("Unwrap chain broken")
	}
	if HasCode(inner, ErrMetaUnparseable) {
		t.Fatal("plain error must not match a code")
	}
}
