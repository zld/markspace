package track

import (
	"errors"
	"testing"
)

func inputs() []Input {
	return []Input{
		{ID: 1, Kind: KindVideo, Timescale: 90000, Codec: "avc1"},
		{ID: 2, Kind: KindAudio, Timescale: 48000, Codec: "mp4a"},
		{ID: 3, Kind: KindSubtitle, Timescale: 1000, Codec: "stpp"},
	}
}

func TestNewPreservesInputIdentity(t *testing.T) {
	m, err := New(inputs(), []uint32{10, 20, 30})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := m.Lookup(2)
	if !ok || e.OutputID != 20 || e.Kind != KindAudio || e.Timescale != 48000 || e.Codec != "mp4a" {
		t.Fatalf("entry lost fields: %+v", e)
	}
	if err := m.RequireComplete(inputs()); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateOutputRejected(t *testing.T) {
	_, err := New(inputs(), []uint32{10, 20, 20})
	if !errors.Is(err, ErrDuplicateMapping) {
		t.Fatalf("got %v", err)
	}
}

func TestMissingMappingRejected(t *testing.T) {
	m, err := New(inputs()[:2], []uint32{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RequireComplete(inputs()); !errors.Is(err, ErrMissingMapping) {
		t.Fatalf("got %v", err)
	}
}
