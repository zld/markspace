package adapter

import (
	"bytes"
	"strings"
	"testing"

	"mediacore/media"
)

func TestRegistryLookupAndNegotiation(t *testing.T) {
	r := DefaultRegistry()
	a, err := r.Lookup("mp4", 1)
	if err != nil || a.Name() != "mp4" || a.Version() != 1 {
		t.Fatalf("lookup failed: %v", err)
	}
	if _, err := r.Lookup("mp4", 99); err == nil {
		t.Fatal("unknown version accepted")
	}
	if _, err := r.Lookup("nope", 1); err == nil {
		t.Fatal("unknown adapter accepted")
	}
	got, err := r.Negotiate("rtp", []Capability{CapRTPWrap, CapReorder})
	if err != nil || got.Name() != "rtp" {
		t.Fatalf("negotiation failed: %v", err)
	}
	_, err = r.Negotiate("rtp", []Capability{CapEditList})
	if err == nil || !media.RejectableError(err) {
		t.Fatalf("incompatible negotiation must be rejectable: %v", err)
	}
	if !strings.Contains(err.Error(), "edit_list") {
		t.Fatalf("error should name missing capability: %v", err)
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NewMP4AdapterV1()); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(NewMP4AdapterV1()); err == nil {
		t.Fatal("duplicate registration accepted")
	}
}

func TestEnforceLimits(t *testing.T) {
	if err := EnforceLimits(16, media.MaxInputDuration, media.MaxParseBytes); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
	for _, tc := range []struct {
		tracks, bytes int
		dur           int64
	}{
		{media.MaxTracks + 1, 1, 1},
		{1, media.MaxParseBytes + 1, 1},
		{1, 1, media.MaxInputDuration + 1},
	} {
		if err := EnforceLimits(tc.tracks, tc.dur, tc.bytes); err == nil {
			t.Fatalf("limit breach accepted: %+v", tc)
		}
	}
}

func TestBoundedReaderStopsAtBudget(t *testing.T) {
	src := bytes.NewReader(bytes.Repeat([]byte{0xAB}, 1024))
	br := NewBoundedReader(src, 128)
	buf := make([]byte, 64)
	if _, err := br.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := br.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := br.Read(buf); err == nil {
		t.Fatal("budget exhaustion did not error")
	}
	n, err := ReadCount(br)
	if err != nil || n != 128 {
		t.Fatalf("read count = %d, %v", n, err)
	}
}

func TestManifestValidateFlagsTimelineErrors(t *testing.T) {
	good := &Manifest{Tracks: []*TrackManifest{{
		TrackID: 1, Samples: []Sample{
			{Index: 0, PTS: 0, Duration: 10},
			{Index: 1, PTS: 10, Duration: 10},
		},
	}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	badDur := &Manifest{Tracks: []*TrackManifest{{Samples: []Sample{{Index: 0, Duration: 0}}}}}
	if err := badDur.Validate(); err == nil {
		t.Fatal("zero duration accepted")
	}
	rollback := &Manifest{Tracks: []*TrackManifest{{Samples: []Sample{
		{Index: 0, PTS: 100, Duration: 10},
		{Index: 1, PTS: 50, Duration: 10},
	}}}}
	err := rollback.Validate()
	if me, ok := media.AsError(err); !ok || me.Code != media.CodePTSRollback {
		t.Fatalf("want pts_rollback, got %v", err)
	}
}
