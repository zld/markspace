package fanin

import (
	"testing"

	"mediad/internal/media"
	"mediad/internal/merr"
)

func TestBoundaryMismatchRejected(t *testing.T) {
	// At the switch, 1080p's nearest block is 40 ms away from 360p's.
	streams := map[string][]media.Block{
		"360p":  stream("360p", map[media.TrackID][]int64{1: {0, 40e6, 80e6}}),
		"1080p": stream("1080p", map[media.TrackID][]int64{1: {0, 80e6}}),
	}
	res, err := Merge(streams, []string{"360p", "1080p"}, Options{
		Plan: []Switch{{Track: 1, AtNs: 40e6, ToRep: "1080p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 1 {
		t.Fatalf("mismatches = %d, want 1", len(res.Mismatches))
	}
	m := res.Mismatches[0]
	if m.Track != 1 || m.FromRep != "360p" || m.ToRep != "1080p" {
		t.Fatalf("mismatch lacks context: %+v", m)
	}
	if m.DeviationNs <= DefaultToleranceNs {
		t.Fatalf("deviation %d not above tolerance", m.DeviationNs)
	}
	if m.TimeNs != 40e6 {
		t.Fatalf("mismatch time = %d", m.TimeNs)
	}
	// The rejected switch must leave every block on 360p.
	for _, b := range res.Blocks {
		if b.Rep != "360p" {
			t.Fatalf("rejected switch moved block to %s", b.Rep)
		}
	}
}

func TestBoundaryWithinToleranceAccepted(t *testing.T) {
	streams := map[string][]media.Block{
		"360p": stream("360p", map[media.TrackID][]int64{1: {0, 40e6, 80e6}}),
		"720p": stream("720p", map[media.TrackID][]int64{1: {5e6, 45e6, 85e6}}),
	}
	res, err := Merge(streams, []string{"360p", "720p"}, Options{
		Plan: []Switch{{Track: 1, AtNs: 40e6, ToRep: "720p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 0 {
		t.Fatalf("unexpected mismatches: %v", res.Mismatches)
	}
	if len(res.Boundaries) != 1 {
		t.Fatal("no accepted boundary recorded")
	}
	if res.MaxBoundaryDeviationNs > DefaultToleranceNs {
		t.Fatalf("accepted deviation %d exceeds tolerance", res.MaxBoundaryDeviationNs)
	}
}

func TestMissingTargetBlockRejected(t *testing.T) {
	// Audio is only carried by 360p; switching it to a representation that
	// does not carry the track at all must be rejected as missing.
	streams := map[string][]media.Block{
		"360p":  stream("360p", map[media.TrackID][]int64{1: {0, 40e6}, 2: {0, 40e6}}),
		"1080p": stream("1080p", map[media.TrackID][]int64{1: {0, 40e6}}),
	}
	res, err := Merge(streams, []string{"360p", "1080p"}, Options{
		Plan: []Switch{{Track: 2, AtNs: 40e6, ToRep: "1080p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 1 {
		t.Fatalf("mismatches = %d, want 1 (%v)", len(res.Mismatches), res.Mismatches)
	}
	if res.Mismatches[0].Reason != "missing" {
		t.Fatalf("reason = %q", res.Mismatches[0].Reason)
	}
	if res.Mismatches[0].Track != 2 {
		t.Fatalf("mismatch track = %d", res.Mismatches[0].Track)
	}
}

func TestOutOfOrderStreamRejected(t *testing.T) {
	streams := map[string][]media.Block{
		"360p": {
			{Track: 1, TimeNs: 0},
			{Track: 1, TimeNs: 40e6},
			{Track: 1, TimeNs: 20e6},
		},
	}
	_, err := Merge(streams, []string{"360p"}, Options{})
	if err == nil {
		t.Fatal("expected rejection of an out-of-order stream")
	}
	if code := merr.CodeOf(err); code != merr.CodeBadInput {
		t.Fatalf("code = %s", code)
	}
}

func TestPlanUnknownRepresentationRejected(t *testing.T) {
	streams := map[string][]media.Block{
		"360p": stream("360p", map[media.TrackID][]int64{1: {0}}),
	}
	_, err := Merge(streams, []string{"360p"}, Options{
		Plan: []Switch{{Track: 1, AtNs: 0, ToRep: "nope"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown representation in plan")
	}
}

func TestLadderOrderNamesUnknownRepRejected(t *testing.T) {
	streams := map[string][]media.Block{
		"360p": stream("360p", map[media.TrackID][]int64{1: {0}}),
	}
	_, err := Merge(streams, []string{"360p", "ghost"}, Options{})
	if err == nil {
		t.Fatal("expected error for ladder order naming a missing stream")
	}
}

func TestPerTrackIndependentSwitch(t *testing.T) {
	streams := map[string][]media.Block{
		"360p": {
			{Track: 1, TimeNs: 0}, {Track: 1, TimeNs: 40e6},
			{Track: 2, TimeNs: 0}, {Track: 2, TimeNs: 40e6},
		},
		"1080p": {
			{Track: 1, TimeNs: 0}, {Track: 1, TimeNs: 40e6},
			{Track: 2, TimeNs: 0}, {Track: 2, TimeNs: 40e6},
		},
	}
	res, err := Merge(streams, []string{"360p", "1080p"}, Options{
		Plan: []Switch{{Track: 2, AtNs: 40e6, ToRep: "1080p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range res.Blocks {
		switch b.Track {
		case 1:
			if b.Rep != "360p" {
				t.Fatalf("video moved to %s without a plan", b.Rep)
			}
		case 2:
			want := "360p"
			if b.TimeNs >= 40e6 {
				want = "1080p"
			}
			if b.Rep != want {
				t.Fatalf("audio at %d from %s want %s", b.TimeNs, b.Rep, want)
			}
		}
	}
}

func TestNoStreamsRejected(t *testing.T) {
	if _, err := Merge(nil, nil, Options{}); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestRejectedSwitchKeepsEveryBlock(t *testing.T) {
	// 1080p is missing the switch instant; the boundary is rejected, and no
	// block may be dropped as a result.
	streams := map[string][]media.Block{
		"360p":  stream("360p", map[media.TrackID][]int64{1: {0, 40e6, 80e6}}),
		"1080p": stream("1080p", map[media.TrackID][]int64{1: {0, 80e6}}),
	}
	res, err := Merge(streams, []string{"360p", "1080p"}, Options{
		Plan: []Switch{{Track: 1, AtNs: 40e6, ToRep: "1080p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 1 {
		t.Fatalf("mismatches = %d, want 1", len(res.Mismatches))
	}
	if len(res.Blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(res.Blocks))
	}
	for _, b := range res.Blocks {
		if b.Rep != "360p" {
			t.Fatalf("rejected switch moved a block to %s", b.Rep)
		}
	}
}

func TestDefaultOrderIsAlphabetical(t *testing.T) {
	times := []int64{0, 40e6}
	streams := map[string][]media.Block{
		"b": stream("b", map[media.TrackID][]int64{1: times}),
		"a": stream("a", map[media.TrackID][]int64{1: times}),
	}
	res, err := Merge(streams, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocks[0].Rep != "a" {
		t.Fatalf("fallback order kept %q", res.Blocks[0].Rep)
	}
}
