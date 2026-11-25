package verify

import (
	"testing"

	"mediasync/internal/model"
)

func fixtureTracks() []TrackSpec {
	return []TrackSpec{
		{TrackID: 1, Timescale: 90000, Duration: 180000, Kind: model.KindVideo,
			KeyframeIndices: []uint32{0, 2}},
		{TrackID: 2, Timescale: 48000, Duration: 96000, Kind: model.KindDialogue},
	}
}

func fixtureFragments() []FragmentSpec {
	return []FragmentSpec{
		{TrackID: 1, BaseDecodeTime: 0, SampleDuration: 90000, SampleCount: 1, Keyframe: true},
		{TrackID: 1, BaseDecodeTime: 90000, SampleDuration: 90000, SampleCount: 1},
		{TrackID: 2, BaseDecodeTime: 0, SampleDuration: 48000, SampleCount: 2},
	}
}

func TestRoundTripPreservesTimingAndKeyframes(t *testing.T) {
	res, err := RoundTrip("video-hi", fixtureTracks(), fixtureFragments())
	if err != nil {
		t.Fatalf("round trip error: %v", err)
	}
	if !res.OK {
		t.Fatalf("round trip mismatch: %v", res.Reasons)
	}
	meta := res.Metadata
	if len(meta.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(meta.Tracks))
	}
	video := meta.Tracks[0]
	if video.Timescale != 90000 || video.Duration != 180000 || video.TrackID != 1 {
		t.Fatalf("video timing not preserved: %+v", video)
	}
	if len(video.KeyframeIndices) != 2 || video.KeyframeIndices[0] != 0 ||
		video.KeyframeIndices[1] != 2 {
		t.Fatalf("keyframe indices not preserved: %v", video.KeyframeIndices)
	}
	if len(meta.Fragments) != 3 || meta.Fragments[0].BaseDecodeTime != 0 ||
		meta.Fragments[1].BaseDecodeTime != 90000 {
		t.Fatalf("fragment tfdt timing not preserved: %+v", meta.Fragments)
	}
	if len(meta.FragmentOffsets) != 3 || meta.FragmentOffsets[0] <= 0 {
		t.Fatalf("fragment offsets must be recorded: %v", meta.FragmentOffsets)
	}
}

func TestRoundTripDetectsMismatch(t *testing.T) {
	// Write a correct stream, then compare it against a deliberately wrong
	// expectation: the comparator must report a concrete reason rather than
	// trusting the input spec.
	data, err := WriteFragmentedMP4(fixtureTracks(), fixtureFragments())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseFragmentedMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Tracks[0].KeyframeIndices[1] != 2 {
		t.Fatalf("parsed keyframe data wrong: %v", meta.Tracks[0].KeyframeIndices)
	}
	wrong := fixtureTracks()
	wrong[0].KeyframeIndices = []uint32{0, 5}
	res := compareTracksOnly("video-hi", wrong, meta)
	if res.OK || len(res.Reasons) == 0 {
		t.Fatal("tampered keyframe expectation must fail the comparison")
	}
}

func TestTruncatedMP4IsLocatedAndIsolated(t *testing.T) {
	data, err := WriteFragmentedMP4(fixtureTracks(), fixtureFragments())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the first top-level box size to claim more bytes than exist.
	corrupt := make([]byte, len(data))
	copy(corrupt, data)
	corrupt[0] = 0x7F // inflate declared size far beyond EOF
	_, err = ParseFragmentedMP4(corrupt)
	if model.Classify(err) != model.KindTruncatedMP4 {
		t.Fatalf("want %s, got %v", model.KindTruncatedMP4, err)
	}
	if model.Locate(err) < 0 {
		t.Fatalf("truncation error must carry byte offset: %v", err)
	}

	// A second, independent representation still parses successfully — one
	// corrupt layer never blocks the others.
	res2, err2 := RoundTrip("dialogue-core", fixtureTracks(), fixtureFragments())
	if err2 != nil || !res2.OK {
		t.Fatalf("sibling representation must survive: err=%v ok=%v", err2, res2.OK)
	}

	// A physically truncated buffer is also rejected.
	_, err = ParseFragmentedMP4(data[:len(data)/2])
	if model.Classify(err) != model.KindTruncatedMP4 {
		t.Fatalf("cut buffer: want %s, got %v", model.KindTruncatedMP4, err)
	}
}

func TestHashesAreDeterministic(t *testing.T) {
	segs := []model.SegmentDescriptor{
		{Source: "dash", Representation: "b", PeriodID: 0, Index: 1,
			Kind: model.KindSubtitle, StartNS: 2_000_000, DurationNS: 1_000_000,
			Payload: []byte("two")},
		{Source: "dash", Representation: "a", PeriodID: 0, Index: 0,
			Kind: model.KindDialogue, StartNS: 0, DurationNS: 1_000_000,
			Payload: []byte("one")},
	}
	first := HashAll(segs)
	second := HashAll(segs)
	if len(first) != 2 || first[0].Representation != "a" {
		t.Fatalf("hash output must be deterministically ordered: %+v", first)
	}
	for i := range first {
		if first[i].SHA256 != second[i].SHA256 || len(first[i].SHA256) != 64 {
			t.Fatalf("hash %d not stable across runs", i)
		}
	}
	// Order-independent set hash must be identical after shuffling.
	shuffled := []model.SegmentDescriptor{segs[1], segs[0]}
	if HashSet(segs) != HashSet(shuffled) {
		t.Fatal("set hash must not depend on slice order")
	}
	// A timing change must change the digest.
	tweaked := segs[0]
	tweaked.DurationNS = 999_999
	if HashSegment(tweaked).SHA256 == first[1].SHA256 {
		t.Fatal("different timing must produce a different hash")
	}
}
