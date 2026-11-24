package demux

import (
	"testing"

	"medpipe/internal/fault"
)

func TestSelectTracksDefaultAndExplicitRules(t *testing.T) {
	m := &Media{Tracks: []*Track{
		{PID: 256, ID: 1, Codec: "avc1", Kind: "video"},
		{PID: 257, ID: 2, Codec: "mp4a", Kind: "audio"},
	}}
	all, err := SelectTracks(m, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("default mappings=%d err=%v", len(all), err)
	}

	rules := []MapRule{{FromPID: 257, ToIndex: 1, Enabled: true}}
	one, err := SelectTracks(m, rules)
	if err != nil || len(one) != 1 {
		t.Fatalf("explicit mapping err=%v", err)
	}
	if one[0].Track.PID != 257 || one[0].OutputIndex != 1 {
		t.Fatalf("bad mapping: %+v", one[0])
	}

	missing := []MapRule{{FromPID: 999, Enabled: true}}
	if _, err := SelectTracks(m, missing); err == nil || err.Code != fault.ErrUnmappableTrack {
		t.Fatalf("missing rule err=%v", err)
	}
}

func TestSelectTracksRejectsUnknownCodec(t *testing.T) {
	m := &Media{Tracks: []*Track{{PID: 1, Codec: "unknown", Kind: "unknown"}}}
	if _, err := SelectTracks(m, nil); err == nil || err.Code != fault.ErrUnknownCodec {
		t.Fatalf("unknown codec err=%v", err)
	}
}
