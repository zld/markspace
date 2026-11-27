package track

import (
	"testing"

	"mediarc/internal/model"
)

func recoveredTrack(id uint32, codec model.Codec, timescale uint32) model.RecoveredTrack {
	return model.RecoveredTrack{
		Track:   model.Track{ID: id, Codec: codec, Timescale: timescale, NotMappable: codec == model.CodecUnknown},
		Trusted: true,
	}
}

func TestMappingOneToOneAccepted(t *testing.T) {
	plan := Plan{
		Outputs: []OutputSpec{{OutputID: 1, Role: "video", Timescale: 90000}, {OutputID: 2, Role: "audio", Timescale: 48000}},
		Sources: []Source{
			{RepID: "r1", Role: "video", Track: recoveredTrack(100, model.CodecAVC1, 90000)},
			{RepID: "r1", Role: "audio", Track: recoveredTrack(102, model.CodecMP4A, 48000)},
		},
	}
	res := plan.Submit()
	if !res.Accepted || len(res.Committed) != 2 {
		t.Fatalf("mapping must be accepted: %+v", res.Rejections)
	}
	if res.Committed[1].Track.Track.ID != 100 || res.Committed[2].Track.Track.ID != 102 {
		t.Fatalf("wrong committed sources: %+v", res.Committed)
	}
}

func TestMappingMissingTrack(t *testing.T) {
	plan := Plan{
		Outputs: []OutputSpec{{OutputID: 1, Role: "video", Timescale: 90000}},
		Sources: []Source{{RepID: "r1", Role: "audio", Track: recoveredTrack(1, model.CodecMP4A, 48000)}},
	}
	res := plan.Submit()
	if res.Accepted {
		t.Fatal("missing video source must reject")
	}
	if len(res.Rejections) != 1 || res.Rejections[0].Kind != model.KindTrackMissing {
		t.Fatalf("want one track_missing rejection, got %+v", res.Rejections)
	}
}

func TestMappingDuplicateSource(t *testing.T) {
	src := Source{RepID: "r1", Role: "video", Track: recoveredTrack(100, model.CodecAVC1, 90000)}
	plan := Plan{
		Outputs: []OutputSpec{{OutputID: 1, Role: "video", Timescale: 90000}, {OutputID: 2, Role: "video", Timescale: 90000}},
		Sources: []Source{src},
	}
	res := plan.Submit()
	if res.Accepted {
		t.Fatal("one source serving two outputs must be rejected")
	}
	found := false
	for _, rj := range res.Rejections {
		if rj.Kind == model.KindTrackDuplicate {
			found = true
		}
	}
	if !found {
		t.Fatalf("want track_duplicate rejection, got %+v", res.Rejections)
	}
}

func TestMappingAmbiguousCandidates(t *testing.T) {
	plan := Plan{
		Outputs: []OutputSpec{{OutputID: 1, Role: "video", Timescale: 90000}},
		Sources: []Source{
			{RepID: "r1", Role: "video", Track: recoveredTrack(100, model.CodecAVC1, 90000)},
			{RepID: "r2", Role: "video", Track: recoveredTrack(200, model.CodecAVC1, 90000)},
		},
	}
	res := plan.Submit()
	if res.Accepted || res.Rejections[0].Kind != model.KindTrackDuplicate {
		t.Fatalf("ambiguous candidates must be rejected as duplicate mapping: %+v", res.Rejections)
	}
}

func TestMappingTimescaleIncompatible(t *testing.T) {
	plan := Plan{
		Outputs: []OutputSpec{{OutputID: 1, Role: "video", Timescale: 1000}},
		Sources: []Source{{RepID: "r1", Role: "video", Track: recoveredTrack(100, model.CodecAVC1, 90000)}},
	}
	res := plan.Submit()
	if res.Accepted || res.Rejections[0].Kind != model.KindTimescaleIncompat {
		t.Fatalf("timescale mismatch must be rejected: %+v", res.Rejections)
	}
}

func TestMappingUnknownCodecRejected(t *testing.T) {
	plan := Plan{
		Outputs: []OutputSpec{{OutputID: 1, Role: "video", Timescale: 90000}},
		Sources: []Source{{RepID: "r1", Role: "video", Track: recoveredTrack(100, model.CodecUnknown, 90000)}},
	}
	res := plan.Submit()
	if res.Accepted || res.Rejections[0].Kind != model.KindTrackMissing {
		t.Fatalf("unmappable codec must be rejected with retained reason: %+v", res.Rejections)
	}
}

func TestMappingRetainsEveryReason(t *testing.T) {
	// One output missing entirely, one with a bad timescale: both reasons
	// must appear on the rejected result rather than failing fast.
	plan := Plan{
		Outputs: []OutputSpec{
			{OutputID: 1, Role: "video", Timescale: 1000},
			{OutputID: 2, Role: "audio", Timescale: 48000},
		},
		Sources: []Source{{RepID: "r1", Role: "video", Track: recoveredTrack(100, model.CodecAVC1, 90000)}},
	}
	res := plan.Submit()
	if res.Accepted {
		t.Fatal("plan must be rejected")
	}
	kinds := map[model.ErrorKind]bool{}
	for _, rj := range res.Rejections {
		kinds[rj.Kind] = true
	}
	if !kinds[model.KindTimescaleIncompat] || !kinds[model.KindTrackMissing] {
		t.Fatalf("both rejection reasons required, got %+v", res.Rejections)
	}
}
