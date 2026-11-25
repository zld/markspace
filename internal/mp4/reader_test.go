package mp4_test

import (
	"testing"

	"mediacore/internal/fixture"
	"mediacore/internal/mp4"
)

func TestNormalFixtureParsesTracksAndWrappingRTP(t *testing.T) {
	dir := t.TempDir()
	set, err := fixture.GenerateNormal(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := mp4.LoadManifest(set.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mp4.ParseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed sources = %d, want 2", len(parsed))
	}
	tr := parsed[0].MP4
	if tr == nil || len(tr.Samples) != fixture.NormalSamples {
		t.Fatalf("mp4 samples = %#v", tr)
	}
	if len(tr.SyncNumbers) != fixture.NormalSamples/fixture.KeyframeEvery {
		t.Fatalf("sync samples = %d", len(tr.SyncNumbers))
	}
	if !tr.HasEdit || len(tr.Edits) != 2 {
		t.Fatalf("edits = %+v", tr.Edits)
	}
	if tr.Edits[0].MediaTime != -1 || tr.Edits[1].MediaTime != fixture.MediaOffsetMS {
		t.Fatalf("edit mapping wrong: %+v", tr.Edits)
	}
	if tr.Samples[0].PTS != fixture.StartPTSOffset {
		t.Fatalf("first PTS = %d, want nonzero offset %d", tr.Samples[0].PTS, fixture.StartPTSOffset)
	}
	rtpTrack := parsed[1].RTP
	if rtpTrack == nil || len(rtpTrack.Packets) != fixture.NormalSamples {
		t.Fatalf("rtp track = %#v", rtpTrack)
	}
	var sawSeqWrap, sawTSWrap bool
	for i := 1; i < len(rtpTrack.Packets); i++ {
		if rtpTrack.Packets[i].UnwrappedSequence > rtpTrack.Packets[i-1].UnwrappedSequence {
			if rtpTrack.Packets[i].Sequence < rtpTrack.Packets[i-1].Sequence {
				sawSeqWrap = true
			}
		}
		if rtpTrack.Packets[i].UnwrappedTimestamp > rtpTrack.Packets[i-1].UnwrappedTimestamp &&
			rtpTrack.Packets[i].Timestamp < rtpTrack.Packets[i-1].Timestamp {
			sawTSWrap = true
		}
	}
	if !sawSeqWrap || !sawTSWrap {
		t.Fatalf("did not observe wraps: seq=%v ts=%v", sawSeqWrap, sawTSWrap)
	}
}
