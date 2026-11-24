package remux

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/demux"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

func buildPlan(t *testing.T, p fixture.Payload) (Plan, *demux.Media) {
	t.Helper()
	media, err := demux.ParseMP4(p.Data, demux.Options{SampleEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := metadata.Merge(media.Events)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]track.Input, len(media.Tracks))
	for i, tr := range media.Tracks {
		inputs[i] = track.Input{ID: tr.ID, Kind: tr.Kind, Timescale: tr.Timescale, Codec: tr.Codec}
	}
	mapping, err := track.Identity(inputs)
	if err != nil {
		t.Fatal(err)
	}
	return Plan{Src: p.Data, Tracks: media.Tracks, Mapping: mapping, Events: merged, MajorBrand: media.MajorBrand}, media
}

func TestRemuxIsPayloadLossless(t *testing.T) {
	p := fixture.BuildValid(fixture.Options{EventDuplicates: true})
	plan, media := buildPlan(t, p)
	var out bytes.Buffer
	res, err := Write(&out, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.PayloadSHA256 != pipelineDigest(media) {
		t.Fatalf("payload digest changed: %s vs %s", res.PayloadSHA256, pipelineDigest(media))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := demux.ParseMP4(written, demux.Options{SampleEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reparsed.Tracks) != len(media.Tracks) {
		t.Fatalf("track count changed: %d", len(reparsed.Tracks))
	}
	if got := reparseDigest(reparsed); got != pipelineDigest(media) {
		t.Fatalf("payload digest after re-parse: %s vs %s", got, pipelineDigest(media))
	}
}

func TestRemuxRejectsIncompleteMapping(t *testing.T) {
	p := fixture.BuildValid(fixture.Options{})
	plan, _ := buildPlan(t, p)
	plan.Mapping = track.Map{Entries: plan.Mapping.Entries[:1]}
	if _, err := Write(&bytes.Buffer{}, plan); err == nil {
		t.Fatal("expected mapping failure before writing")
	}
}

func TestEventTimesAreExact(t *testing.T) {
	// A 90000 Hz event that is not representable in a 1000 Hz movie timescale
	// must be rejected, never rounded.
	plan := Plan{Timescale: 1000}
	if got := plan.movieTimescale(); got != 1000 {
		t.Fatalf("override ignored: %d", got)
	}
	if _, err := eventTime(newTime(90000, 90000), 1000); err != nil {
		t.Fatalf("1s at 90000 Hz should be exact in 1000: %v", err)
	}
	if _, err := eventTime(newTime(1, 44100), 1000); err == nil {
		t.Fatal("expected non-exact time to be rejected")
	}
}

func newTime(ticks int64, timescale uint32) clock.Time {
	return clock.New(ticks, timescale)
}

// pipelineDigest mirrors the canonical sample-order payload digest without
// importing the pipeline package (which imports remux).
func pipelineDigest(media *demux.Media) string {
	h := sha256.New()
	for _, tr := range media.Tracks {
		for _, s := range tr.Samples {
			h.Write(s.Data)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func reparseDigest(media *demux.Media) string { return pipelineDigest(media) }

var _ = hex.EncodeToString
