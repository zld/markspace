package audit_test

import (
	"testing"

	"mediacore"
	"mediacore/audit"
	"mediacore/timeline"
)

func TestHashDeterminismAndIndexSensitivity(t *testing.T) {
	hasher := timeline.NewSegmentHasher()
	base := &timeline.Segment{
		ID: 1, StartNS: 0, EndNS: 100, InputFrom: 0, InputTo: 50,
		Events: []mediacore.Event{{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM,
			Time: 10, Seq: 0, Samples: 1, Rate: 8000, Channels: 1, Payload: []byte{0xab}}},
	}
	h1 := hasher.HashHex(base)
	h2 := hasher.HashHex(base)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash not deterministic or wrong shape: %q", h1)
	}
	// The assigned index offset is part of the fingerprint.
	shifted := *base
	shifted.IndexOff = 4096
	if hasher.HashHex(&shifted) == h1 {
		t.Fatal("hash ignored index offset")
	}
	// Different recovery action must change the hash.
	acted := *base
	acted.Actions = []timeline.Action{{Kind: "recovered_silence", Track: mediacore.TrackAudio1,
		Offset: 10, Detail: "gap"}}
	if hasher.HashHex(&acted) == h1 {
		t.Fatal("hash ignored recovery action")
	}
}

func TestVerifyReplayAndEqualRuns(t *testing.T) {
	mk := func(id int, start int64, payload byte) *timeline.Segment {
		return &timeline.Segment{ID: id, StartNS: start, EndNS: start + 100,
			Events: []mediacore.Event{{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM,
				Time: start, Seq: uint64(id), Payload: []byte{payload}}}}
	}
	hasher := timeline.NewSegmentHasher()
	before := []*timeline.Segment{mk(0, 0, 1), mk(1, 100, 2)}
	for _, s := range before {
		s.Hash = hasher.Hash(s)
	}
	// After replay: segment 0 rebuilt (different payload), segment 1 identical.
	after := []*timeline.Segment{mk(0, 0, 9), mk(1, 100, 2)}
	for _, s := range after {
		s.Hash = hasher.Hash(s)
	}
	compared, matched, changed := audit.VerifyReplay(before, after)
	if compared != 2 || matched != 1 || changed != 1 {
		t.Fatalf("replay verify: compared=%d matched=%d changed=%d", compared, matched, changed)
	}
	run1, run2 := audit.HashAll(before), audit.HashAll(before)
	if !audit.EqualRuns(run1, run2) {
		t.Fatal("identical runs reported unequal")
	}
	if audit.EqualRuns(run1, audit.HashAll(after)) {
		t.Fatal("different runs reported equal")
	}
}
