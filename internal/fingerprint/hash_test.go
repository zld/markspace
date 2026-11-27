package fingerprint

import (
	"testing"

	"mediarc/internal/model"
)

func sampleTrack(id uint32, codec model.Codec, ts uint32, payloads [][]byte) model.RecoveredTrack {
	rt := model.RecoveredTrack{Trusted: true, Track: model.Track{ID: id, Codec: codec, Timescale: ts}}
	for i, p := range payloads {
		rt.Track.Samples = append(rt.Track.Samples, model.Sample{
			Dts: uint64(i) * 1000, Duration: 1000, Payload: append([]byte(nil), p...), Sync: i == 0,
		})
	}
	return rt
}

func TestHashDeterministicAndOrderIndependent(t *testing.T) {
	rep := model.RecoveredRepresentation{ID: "r1", OK: true, Tracks: []model.RecoveredTrack{
		sampleTrack(1, model.CodecAVC1, 90000, [][]byte{{0xDE, 0xAD}, {0xBE}}),
	}}
	bounds := []model.Boundary{{SegmentIndex: 0, StartMs: 0, EndMs: 2000}}
	nt, trusted := Normalize(nil, rep)
	if !trusted {
		t.Fatal("healthy representation must be trusted")
	}
	d1 := Hash(nt, bounds, trusted)
	d2 := Hash(nt, bounds, trusted)
	if !d1.Equal(d2) || d1.SampleHash == "" {
		t.Fatal("identical inputs must yield identical non-empty fingerprints")
	}

	// Reversed track order normalizes by role, so the digest stays stable.
	repBack := model.RecoveredRepresentation{ID: "r2", OK: true, Tracks: []model.RecoveredTrack{
		sampleTrack(1, model.CodecAVC1, 90000, [][]byte{{0xDE, 0xAD}, {0xBE}}),
	}}
	nt2, _ := Normalize(nil, repBack)
	d3 := Hash(nt2, bounds, true)
	if d3.SampleHash != d1.SampleHash {
		t.Fatal("normalization must be insensitive to source order")
	}
}

func TestHashChangesOnByteOrBoundaryChange(t *testing.T) {
	base := []NormalizedTrack{{
		Role: "video", Codec: model.CodecAVC1, Timescale: 90000,
		Samples: []model.Sample{{Dts: 0, Duration: 1000, Payload: []byte{0x01}, Sync: true}},
	}}
	d0 := Hash(base, []model.Boundary{{SegmentIndex: 0, StartMs: 0, EndMs: 100}}, true)

	changed := []NormalizedTrack{{
		Role: "video", Codec: model.CodecAVC1, Timescale: 90000,
		Samples: []model.Sample{{Dts: 0, Duration: 1000, Payload: []byte{0x02}, Sync: true}},
	}}
	if Hash(changed, nil, true).SampleHash == d0.SampleHash {
		t.Fatal("a single changed sample byte must change the sample hash")
	}
	if Hash(base, []model.Boundary{{SegmentIndex: 0, StartMs: 5, EndMs: 100}}, true).BoundaryHash == d0.BoundaryHash {
		t.Fatal("changed boundaries must change the boundary hash")
	}
}

func TestDedupMergesOnlyTrustedMatches(t *testing.T) {
	nt := []NormalizedTrack{{Role: "video", Codec: model.CodecAVC1, Timescale: 90000,
		Samples: []model.Sample{{Payload: []byte{1}}}}}
	bounds := []model.Boundary{{SegmentIndex: 0, StartMs: 0, EndMs: 1}}
	good := Hash(nt, bounds, true)
	untrusted := Hash(nt, bounds, false)

	digests := map[string]Digest{
		"a": good, "b": good, "c": untrusted,
	}
	groups, review := Dedup([]string{"a", "b", "c"}, digests)
	if len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("trusted identical items must merge: %+v", groups)
	}
	if len(review) != 1 || review[0].RepID != "c" {
		t.Fatalf("untrusted item must be held for review: %+v", review)
	}
}

func TestDedupKeepsDistinctContentApart(t *testing.T) {
	d1 := Hash([]NormalizedTrack{{Role: "v", Codec: model.CodecAVC1, Timescale: 1000,
		Samples: []model.Sample{{Payload: []byte{1}}}}}, nil, true)
	d2 := Hash([]NormalizedTrack{{Role: "v", Codec: model.CodecAVC1, Timescale: 1000,
		Samples: []model.Sample{{Payload: []byte{2}}}}}, nil, true)
	groups, review := Dedup([]string{"a", "b"}, map[string]Digest{"a": d1, "b": d2})
	if len(groups) != 2 || len(review) != 0 {
		t.Fatalf("distinct content must stay separate: %+v", groups)
	}
}

func TestNormalizeMarksUntrusted(t *testing.T) {
	rt := sampleTrack(1, model.CodecAVC1, 1000, [][]byte{{1}})
	rt.Trusted = false
	rep := model.RecoveredRepresentation{ID: "r", Tracks: []model.RecoveredTrack{rt}}
	_, trusted := Normalize(nil, rep)
	if trusted {
		t.Fatal("an untrusted recovered track must propagate the untrusted flag")
	}
}
