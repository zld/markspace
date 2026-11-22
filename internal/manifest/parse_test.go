package manifest

import (
	"bytes"
	"strings"
	"testing"

	"github.com/medialab/medialab/internal/clock"
)

func sampleManifest() *Manifest {
	return &Manifest{
		Version: 1,
		Seed:    42,
		TickHz:  clock.TicksPerSecond,
		Tracks: []TrackSpec{
			{
				ID: "v0", Kind: KindVideo, ClockRate: 90_000, SSRC: 11,
				StartMs: 3_600_000, DurationMs: 72_000,
				Keyframes: []int64{0, 5_000, 10_000},
			},
			{
				ID: "a0", Kind: KindAudio, ClockRate: 48_000, SSRC: 22,
				StartMs: 3_600_000, DurationMs: 72_000,
			},
		},
		Edits: []Edit{
			{Track: "v0", SourceStartMs: 0, TimelineStartMs: 0, DurationMs: 60_000},
			{Track: "v0", SourceStartMs: 60_000, TimelineStartMs: 60_000, DurationMs: 12_000},
			{Track: "a0", SourceStartMs: 0, TimelineStartMs: 0, DurationMs: 72_000},
		},
	}
}

func roundTrip(t *testing.T, m *Manifest) *Manifest {
	t.Helper()
	var buf bytes.Buffer
	if err := m.Write(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(&buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return got
}

func TestRoundTripAndNonZeroStart(t *testing.T) {
	got := roundTrip(t, sampleManifest())
	if got.Tracks[0].StartMs != 3_600_000 {
		t.Fatalf("non-zero start lost: %d", got.Tracks[0].StartMs)
	}
	out, err := got.Resolve("v0", 1_500)
	if err != nil {
		t.Fatal(err)
	}
	if out != 1_500 {
		t.Fatalf("resolve = %d, want 1500", out)
	}
}

func TestTruncatedAndMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"cut short":  `{"version":1,"tracks":[`,
		"no tracks":  `{"version":1,"tick_hz":1000,"tracks":[],"edits":[]}`,
		"no edits":   `{"version":1,"tick_hz":1000,"tracks":[{"id":"v0","kind":"video","clock_rate":90000,"start_ms":0,"duration_ms":100}],"edits":[]}`,
		"trailing":   `{"version":1,"tick_hz":1000,"tracks":[],"edits":[]} {}`,
	}
	for name, doc := range cases {
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Fatalf("%s: expected error", name)
		} else if me, ok := clock.As(err); !ok {
			t.Fatalf("%s: error without location metadata: %v", name, err)
		} else if me.Stage != "manifest" {
			t.Fatalf("%s: stage = %q", name, me.Stage)
		}
	}
	if _, err := Parse(strings.NewReader(cases["cut short"])); err != nil {
		me, _ := clock.As(err)
		if me.Kind != clock.KindTruncated {
			t.Fatalf("cut short kind = %s, want truncated", me.Kind)
		}
	}
}

func TestValidationFailuresCarryTrackAndTick(t *testing.T) {
	bad := sampleManifest()
	bad.Tracks[0].Keyframes = []int64{10, 10}
	_, err := roundTripErr(t, bad)
	me := wantErr(t, err, clock.KindNonMonotonic, "v0")
	if me.AtTick != 3_600_010 {
		t.Fatalf("keyframe error tick = %d", me.AtTick)
	}

	bad = sampleManifest()
	bad.Tracks[0].StartMs = -5
	me = wantErr(t, mustValidate(t, bad), clock.KindNegativeTime, "v0")

	bad = sampleManifest()
	bad.Tracks[0].Keyframes = []int64{999_999}
	me = wantErr(t, mustValidate(t, bad), clock.KindOutOfRange, "v0")
	if me.AtTick <= 3_600_000 {
		t.Fatalf("out-of-range keyframe tick not absolute: %d", me.AtTick)
	}

	bad = sampleManifest()
	bad.Edits = append(bad.Edits, Edit{Track: "v0", SourceStartMs: 0, TimelineStartMs: 1000, DurationMs: 10})
	me = wantErr(t, mustValidate(t, bad), clock.KindOverlap, "v0")
	if me.AtTick != 1000 {
		t.Fatalf("overlap tick = %d, want 1000", me.AtTick)
	}

	bad = sampleManifest()
	bad.Edits[0].Track = "ghost"
	me = wantErr(t, mustValidate(t, bad), clock.KindOutOfRange, "ghost")

	bad = sampleManifest()
	bad.Edits[1].DurationMs = 999_999
	me = wantErr(t, mustValidate(t, bad), clock.KindOutOfRange, "v0")
}

func TestResolveOutsideEdits(t *testing.T) {
	m := sampleManifest()
	// Endpoints are exclusive: the last v0 edit covers [60000,72000).
	if _, err := m.Resolve("v0", 72_000); err == nil {
		t.Fatal("position beyond last edit must fail")
	} else {
		me := wantErr(t, err, clock.KindOutOfRange, "v0")
		if me.AtTick != 3_600_000+72_000 {
			t.Fatalf("absolute tick = %d", me.AtTick)
		}
	}
	if _, err := m.Resolve("unknown", 0); err == nil {
		t.Fatal("unknown track must fail")
	}
}

func TestWrongTimeBase(t *testing.T) {
	bad := sampleManifest()
	bad.TickHz = 90_000
	wantErr(t, mustValidate(t, bad), clock.KindOutOfRange, "")
}

func roundTripErr(t *testing.T, m *Manifest) (*Manifest, error) {
	t.Helper()
	var buf bytes.Buffer
	if err := m.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return Parse(&buf)
}

func mustValidate(t *testing.T, m *Manifest) error {
	t.Helper()
	_, err := roundTripErr(t, m)
	return err
}

func wantErr(t *testing.T, err error, kind clock.ErrorKind, track string) *clock.MediaError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", kind)
	}
	me, ok := clock.As(err)
	if !ok {
		t.Fatalf("plain error, want MediaError: %v", err)
	}
	if me.Kind != kind {
		t.Fatalf("kind = %s, want %s (%v)", me.Kind, kind, err)
	}
	if track != "" && me.Track != track {
		t.Fatalf("track = %q, want %q", me.Track, track)
	}
	return me
}
