package manifest

import (
	"strings"
	"testing"

	"mediapipe/internal/clock"
	"mediapipe/internal/model"
)

const goodManifest = `{
  "tracks": [
    {
      "id": "vid-1", "kind": "video", "fps": 30,
      "start_offset_units": 5, "start_offset_rate": 1000,
      "frames": [
        {"index":0,"name":"f000.raw","length":128,"sha256":"%[1]s","time_units":0,"time_rate":90000,"color":{"label":"bt709","source":"manifest"},"rtp_seq":10,"rtp_timestamp":90000},
        {"index":1,"name":"f001.raw","length":128,"sha256":"%[1]s","time_units":3001,"time_rate":90000,"color":{"label":"bt709","source":"manifest"},"rtp_seq":11,"rtp_timestamp":93001}
      ]
    },
    {
      "id": "aud-1", "kind": "audio", "sample_rate": 48000, "channels": 6,
      "layout": "5.1", "loudness_lufs": -23,
      "frames": [
        {"index":0,"name":"a000.raw","length":4096,"sha256":"%[1]s","time_units":0,"time_rate":48000,"color":{"label":"","source":""},"rtp_seq":0,"rtp_timestamp":0}
      ]
    }
  ]
}`

func zeroHash() string {
	var h model.Hash
	return h.String()
}

func TestParseMapsTimeline(t *testing.T) {
	r := strings.NewReader(strings.ReplaceAll(goodManifest, "%[1]s", zeroHash()))
	mts, err := Parse(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(mts) != 2 || mts[0].Track.ID != "aud-1" || mts[1].Track.ID != "vid-1" {
		t.Fatalf("tracks not sorted: %v", mts)
	}
	vid := mts[1]
	// Non-zero 5 ms start maps to 450 ticks.
	if vid.Start90k != clock.Ticks(450) {
		t.Errorf("start = %d, want 450", vid.Start90k)
	}
	// Frame 0 at 0 units -> 450; frame 1 at 3001/90000 -> +3001.
	if vid.Frames[0].At90k != 450 || vid.Frames[1].At90k != 3451 {
		t.Errorf("mapped ticks = %d, %d", vid.Frames[0].At90k, vid.Frames[1].At90k)
	}
	if vid.Frames[0].NextAt != 3451 {
		t.Errorf("NextAt = %d", vid.Frames[0].NextAt)
	}
	if vid.Frames[0].Frame.RTP.SequenceNumber != 10 || vid.Frames[0].Frame.RTP.Timestamp != 90000 {
		t.Errorf("rtp header not preserved: %+v", vid.Frames[0].Frame.RTP)
	}
	// SSRC must be deterministic across parses.
	r2 := strings.NewReader(strings.ReplaceAll(goodManifest, "%[1]s", zeroHash()))
	mts2, _ := Parse(r2)
	if mts2[1].Frames[0].Frame.RTP.SSRC != vid.Frames[0].Frame.RTP.SSRC {
		t.Fatal("SSRC not stable across parses")
	}
}

func TestParseRejectsRegression(t *testing.T) {
	bad := `{"tracks":[{"id":"v","kind":"video","fps":30,"frames":[
	  {"index":0,"name":"a","length":1,"sha256":"` + zeroHash() + `","time_units":100,"time_rate":90000,"rtp_seq":0,"rtp_timestamp":0},
	  {"index":1,"name":"b","length":1,"sha256":"` + zeroHash() + `","time_units":50,"time_rate":90000,"rtp_seq":1,"rtp_timestamp":1}
	]}]}`
	_, err := Parse(strings.NewReader(bad))
	me := model.AsMediaError(err)
	if me == nil || me.Type != model.ErrTimestampBack || me.FrameIndex != 1 || me.TrackID != "v" {
		t.Fatalf("got %v, want located timestamp_regression", err)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	if _, err := Parse(strings.NewReader("{not json")); model.AsMediaError(err) == nil {
		t.Fatal("invalid JSON must classify as MediaError")
	}
	empty := `{"tracks":[]}`
	if _, err := Parse(strings.NewReader(empty)); model.AsMediaError(err) == nil {
		t.Fatal("empty manifest must be rejected")
	}
	badHash := `{"tracks":[{"id":"v","kind":"video","fps":30,"frames":[
	  {"index":0,"name":"a","length":1,"sha256":"zz","time_units":0,"time_rate":90000,"rtp_seq":0,"rtp_timestamp":0}
	]}]}`
	if me := model.AsMediaError(mustParse(t, badHash)); me == nil || me.Type != model.ErrInvalidConfig {
		t.Fatalf("bad hash: %v", me)
	}
}

func mustParse(t *testing.T, s string) error {
	t.Helper()
	_, err := Parse(strings.NewReader(s))
	return err
}
