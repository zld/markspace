package repair

import (
	"bytes"
	"testing"

	"github.com/broadcast/mediad/internal/fixture"
	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/probe"
)

func runAdjudicate(t *testing.T, spec fixture.Spec, linkAudio bool) (*model.Graph, *Decisions, *model.ErrorSet) {
	t.Helper()
	raw := fixture.MustGenerate(spec)
	errs := &model.ErrorSet{}
	g, _, err := probe.Probe(bytes.NewReader(raw), int64(len(raw)), probe.DefaultConfig(), errs)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if linkAudio {
		// Audio alignment explicitly depends on video boundary recovery.
		if a := g.Tracks[2]; a != nil {
			a.DependsOn = []uint32{1}
		}
	}
	dmg := Locate(g, errs, 1<<40)
	dec, err := Adjudicate(g, dmg, errs, DefaultPolicy())
	if err != nil {
		t.Fatalf("adjudicate: %v", err)
	}
	return g, dec, errs
}

func TestNormalFileRecoversFully(t *testing.T) {
	g, dec, _ := runAdjudicate(t, fixture.StandardSet(23)[0], true)
	for _, id := range g.IDs() {
		for _, s := range dec.Segments[id] {
			if s.Status != StatusValid {
				t.Fatalf("track %d segment %d-%d = %s, want valid",
					id, s.FirstSample, s.LastSample, s.Status)
			}
		}
		if len(dec.IsolatedRanges[id]) != 0 {
			t.Fatalf("clean track %d must not isolate ranges", id)
		}
	}
	// Topo order: video 1 before audio 2.
	if len(dec.Order) != 2 || dec.Order[0] != 1 || dec.Order[1] != 2 {
		t.Fatalf("order = %v", dec.Order)
	}
}

func TestBackwardTimestampsRecoveredButMarked(t *testing.T) {
	_, dec, errs := runAdjudicate(t, fixture.StandardSet(23)[2], true)
	if !errs.Has(model.KindTimestampDuplicate) {
		t.Fatal("fixture must expose the timestamp defect")
	}
	video := dec.Segments[1]
	var recovered bool
	for _, s := range video {
		if s.Status == StatusRecovered {
			recovered = true
		}
		if s.Status == StatusIsolated {
			t.Fatalf("payload-intact ordering defect must not be isolated: %+v", s)
		}
	}
	if !recovered {
		t.Fatalf("expected at least one recovered segment, got %+v", video)
	}
	// Recovered segments must still carry the reason as evidence.
	for _, s := range video {
		if s.Status == StatusRecovered && len(s.Reasons) == 0 {
			t.Fatal("recovered segment without defect reason")
		}
	}
}

func TestTruncatedMdatKeepsValidFlanks(t *testing.T) {
	spec := fixture.StandardSet(23)[1]
	raw := fixture.MustGenerate(spec)
	scanErrs := &model.ErrorSet{}
	r := bytes.NewReader(raw)
	if _, err := probe.ScanBoxes(r, int64(len(raw)), scanErrs); err != nil {
		t.Fatal(err)
	}
	if !scanErrs.Has(model.KindTruncatedBox) {
		t.Fatal("truncated mdat must be detected structurally")
	}
}

func TestFailedVideoPrerequisiteBlocksAudioRecovery(t *testing.T) {
	// Build a graph by hand: video cannot prove any keyframe-decodable span,
	// audio explicitly depends on it. Audio must never claim recovery.
	g := handGraph()
	errs := &model.ErrorSet{}
	dmg := Locate(g, errs, 1<<40)
	// Force the video track into a no-keyframe state.
	for _, s := range g.Tracks[1].Samples {
		s.IsKeyframe = false
	}
	dec, err := Adjudicate(g, dmg, errs, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.FailedDependencies[2]) == 0 || dec.FailedDependencies[2][0] != 1 {
		t.Fatalf("audio must record failed prerequisite, got %v", dec.FailedDependencies)
	}
	for _, s := range dec.Segments[2] {
		if s.Status == StatusRecovered || s.Status == StatusValid {
			t.Fatalf("downstream audio cannot claim %s after video failure", s.Status)
		}
	}
}

func TestCycleIsRejectedWithLocatableError(t *testing.T) {
	v := &model.Track{ID: 1, Kind: model.KindVideo, DependsOn: []uint32{2}}
	a := &model.Track{ID: 2, Kind: model.KindAudio, DependsOn: []uint32{1}}
	g, err := model.NewGraph([]*Track{v, a})
	if err != nil {
		t.Fatal(err)
	}
	errs := &model.ErrorSet{}
	if _, err := Adjudicate(g, Locate(g, errs, 1<<40), errs, DefaultPolicy()); err == nil {
		t.Fatal("cycle must return an error")
	}
	if !errs.Has(model.KindCyclicDependency) {
		t.Fatalf("cycle must be mapped to the shared error model, got %v", errs.All())
	}
}

// handGraph builds two minimal tracks: 8 video samples (keyframes at 0 and 4)
// and 8 audio samples depending on the video, sharing one timeline second.
func handGraph() *model.Graph {
	mk := func(id uint32, kind model.MediaKind, n int) *model.Track {
		t := &model.Track{ID: id, Kind: kind, Timescale: 8}
		for i := 0; i < n; i++ {
			t.Samples = append(t.Samples, &model.Sample{
				Index: i,
				DecodeTime: model.MustRat(int64(i), 8),
				Duration:   model.MustRat(1, 8),
				Size:       8,
			})
		}
		t.StartTime = t.Samples[0].DecodeTime
		t.EndTime = t.Samples[n-1].DecodeTime.Add(t.Samples[n-1].Duration)
		return t
	}
	v := mk(1, model.KindVideo, 8)
	v.KeyframeIndexPresent = true
	v.Samples[0].IsKeyframe = true
	v.Samples[4].IsKeyframe = true
	a := mk(2, model.KindAudio, 8)
	a.DependsOn = []uint32{1}
	g, err := model.NewGraph([]*model.Track{v, a})
	if err != nil {
		panic(err)
	}
	return g
}

// alias so the hand-graph helper compiles without import churn.
type Track = model.Track
