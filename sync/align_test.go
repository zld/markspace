package sync

import (
	"testing"

	"mediarc/model"
)

func cues(starts ...int64) []CueRef {
	out := make([]CueRef, len(starts))
	for i, start := range starts {
		out[i] = CueRef{Index: i + 1, StartNS: start, EndNS: start + 500_000_000}
	}
	return out
}

func anchors(times ...int64) []Anchor {
	out := make([]Anchor, len(times))
	for i, t := range times {
		out[i] = Anchor{TimeNS: t, Label: "a" + string(rune('0'+i))}
	}
	return out
}

func TestAlignWithinTolerance(t *testing.T) {
	aligner, err := New(Config{ToleranceNS: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	result := aligner.Align(
		cues(0, 1_000_000_000, 2_000_800_000),
		anchors(0, 1_000_000_000, 2_000_000_000),
	)
	if !result.OK() {
		t.Fatalf("unexpected drift: %+v", result.Issues)
	}
	if result.Drifted != 0 || result.Unanchored != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.MaxErrorNS != 800_000 {
		t.Fatalf("max error = %d", result.MaxErrorNS)
	}
	if result.WithinTolerance != 3 {
		t.Fatalf("within tolerance = %d", result.WithinTolerance)
	}
}

func TestAlignBeyondToleranceFlagsWithoutMoving(t *testing.T) {
	aligner, _ := New(Config{ToleranceNS: 1_000_000})
	input := cues(1_002_000_000)
	result := aligner.Align(input, anchors(1_000_000_000))
	if result.Drifted != 1 {
		t.Fatalf("drifted = %d", result.Drifted)
	}
	if len(result.Issues) != 1 || !model.IsCode(result.Issues[0], model.CodeAlignmentDrift) {
		t.Fatalf("issues = %v", result.Issues)
	}
	pair := result.Pairs[0]
	if pair.ErrorNS != 2_000_000 || pair.AbsError != 2_000_000 {
		t.Fatalf("pair = %+v", pair)
	}
	// The cue must keep its own timestamp; alignment reports, never rewrites.
	if pair.Cue.StartNS != 1_002_000_000 || input[0].StartNS != 1_002_000_000 {
		t.Fatalf("cue time was moved: %+v", pair.Cue)
	}
	if result.MaxErrorNS != 2_000_000 {
		t.Fatalf("max error = %d", result.MaxErrorNS)
	}
}

func TestAlignNegativeError(t *testing.T) {
	aligner, _ := New(Config{ToleranceNS: 1_000})
	result := aligner.Align(cues(999_000_000), anchors(1_000_000_000))
	if len(result.Pairs) != 1 {
		t.Fatal("missing pair")
	}
	if result.Pairs[0].ErrorNS != -1_000_000 {
		t.Fatalf("error = %d", result.Pairs[0].ErrorNS)
	}
	if result.Pairs[0].AbsError != 1_000_000 {
		t.Fatalf("abs error = %d", result.Pairs[0].AbsError)
	}
}

func TestAlignNearestAnchorTiePrefersEarlier(t *testing.T) {
	aligner, _ := New(Config{ToleranceNS: 1})
	result := aligner.Align(cues(50), []Anchor{{TimeNS: 0, Label: "early"}, {TimeNS: 100, Label: "late"}})
	if result.Pairs[0].Anchor.Label != "early" {
		t.Fatalf("anchor = %q, want early", result.Pairs[0].Anchor.Label)
	}
}

func TestAlignUnanchored(t *testing.T) {
	passive, _ := New(Config{ToleranceNS: 1_000})
	result := passive.Align(cues(0), nil)
	if result.Unanchored != 1 || !result.OK() {
		t.Fatalf("passive result = %+v", result)
	}
	if result.Pairs[0].Issue != nil {
		t.Fatal("unexpected issue without RequireAnchor")
	}
	strict, _ := New(Config{ToleranceNS: 1_000, RequireAnchor: true})
	strictResult := strict.Align(cues(0), nil)
	if strictResult.OK() || len(strictResult.Issues) != 1 {
		t.Fatalf("strict result = %+v", strictResult)
	}
	if !model.IsCode(strictResult.Issues[0], model.CodeAlignmentDrift) {
		t.Fatalf("issue = %v", strictResult.Issues[0])
	}
}

func TestAlignConfigValidation(t *testing.T) {
	if _, err := New(Config{ToleranceNS: -1}); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("negative tolerance: %v", err)
	}
	aligner, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if aligner.Tolerance() != 1_000_000 {
		t.Fatalf("default tolerance = %d", aligner.Tolerance())
	}
}

func TestAlignDeterministic(t *testing.T) {
	aligner, _ := New(Config{ToleranceNS: 1_000_000})
	cueRefs := cues(0, 1_000_000, 2_000_000, 3_000_000)
	anchorRefs := anchors(0, 1_000_000, 2_000_000, 3_000_000)
	first := aligner.Align(cueRefs, anchorRefs)
	second := aligner.Align(cueRefs, anchorRefs)
	if len(first.Pairs) != len(second.Pairs) {
		t.Fatal("pair count differs")
	}
	for i := range first.Pairs {
		if first.Pairs[i].Anchor.Label != second.Pairs[i].Anchor.Label ||
			first.Pairs[i].ErrorNS != second.Pairs[i].ErrorNS {
			t.Fatalf("pair %d differs between runs", i)
		}
	}
}
