package adapter_test

import (
	"testing"

	"mediad/internal/adapter"
	_ "mediad/internal/adapter/mp4"
	_ "mediad/internal/adapter/rtp"
	"mediad/internal/model"
)

func TestRegistryVersions(t *testing.T) {
	versions := adapter.RegisteredVersions()
	if versions["mp4"] != "mp4-v1" || versions["rtp"] != "rtp-v1" {
		t.Fatalf("versions = %+v", versions)
	}
}

func TestRegistryRejectsUnknown(t *testing.T) {
	_, err := adapter.Get("av1-in-wild")
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindUnregisteredParser {
		t.Fatalf("got %v", err)
	}
}

func TestFormatForPath(t *testing.T) {
	cases := map[string]string{
		"a/b/1.m4s": "mp4",
		"x.MP4":     "mp4",
		"cap.rtp":   "rtp",
	}
	for path, want := range cases {
		if got := adapter.FormatForPath(path); got != want {
			t.Errorf("FormatForPath(%q) = %q want %q", path, got, want)
		}
	}
}

func TestParseAnnotatesLocation(t *testing.T) {
	_, err := adapter.Parse(adapter.MediaInput{Path: "missing.rtp", Representation: "r", Entry: 7})
	se, ok := model.AsSource(err)
	if !ok {
		t.Fatalf("got %v", err)
	}
	if se.Loc.Representation != "r" || se.Loc.Entry != 7 || se.Loc.Segment != "missing.rtp" {
		t.Fatalf("location not annotated: %+v", se.Loc)
	}
}
