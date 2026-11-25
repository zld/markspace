package fixture

import (
	"bytes"
	"testing"
	"time"

	"rtptrace/internal/manifest"
)

func testOpts(fault string) Options {
	return Options{Seed: 17, Duration: 120 * time.Second, SegmentDuration: 6 * time.Second,
		Bitrates: []int{1600000, 800000}, Fault: fault}
}

func TestGenerateNormalFixtureAndParseMP4(t *testing.T) {
	f, err := Generate(testOpts(""))
	if err != nil {
		t.Fatal(err)
	}
	if f.Manifest.Seed != 17 || len(f.Manifest.Tracks) != 3 {
		t.Fatalf("manifest=%+v", f.Manifest)
	}
	if f.Manifest.Representations[0].ID != "rep-800k" {
		t.Fatalf("bitrates not sorted: %+v", f.Manifest.Representations)
	}
	mp4, err := manifest.ReadMP4(bytes.NewReader(f.MP4))
	if err != nil {
		t.Fatalf("ReadMP4: %v", err)
	}
	if mp4.MovieTimescale != 1000 || len(mp4.Tracks) != 3 {
		t.Fatalf("mp4=%+v", mp4)
	}
	var video *manifest.MP4Track
	for i := range mp4.Tracks {
		if mp4.Tracks[i].ID == 1 {
			video = &mp4.Tracks[i]
		}
	}
	if video == nil || video.Timescale != 24000 || video.SampleTable.SampleCount != 120 {
		t.Fatalf("video track=%+v", video)
	}
	if len(video.EditList.Edits) != 1 || video.EditList.Edits[0].MediaTime != 48000 {
		t.Fatalf("edit list=%+v", video.EditList)
	}
	if len(f.RTPPackets) < 110 {
		t.Fatalf("rtp packets=%d", len(f.RTPPackets))
	}
}

func TestGenerateTruncatedFixture(t *testing.T) {
	f, err := Generate(testOpts("truncated-stbl"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = manifest.Parse(f.ManifestJSON)
	var corrupt *manifest.CorruptionError
	if !asCorruption(err, &corrupt) {
		t.Fatalf("expected corruption, got %v", err)
	}
	if corrupt.TrackID != 1 || corrupt.Sample != 37 {
		t.Fatalf("corrupt=%+v", corrupt)
	}
}

func asCorruption(err error, target **manifest.CorruptionError) bool {
	for err != nil {
		if c, ok := err.(*manifest.CorruptionError); ok {
			*target = c
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
