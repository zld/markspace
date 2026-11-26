package clip

import (
	"os"
	"path/filepath"
	"testing"

	"replaypipe/internal/fixtures"
	"replaypipe/internal/index"
	"replaypipe/internal/metadata"
	"replaypipe/internal/mp4box"
	"replaypipe/internal/timebase"
)

func testMedia(t *testing.T) *index.Media {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(path, mp4box.Build(fixtures.BuildTracks(), mp4box.DefaultOpts()), 0o644); err != nil {
		t.Fatal(err)
	}
	media, err := index.ParseFile(path, index.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return media
}

func testEvents() []metadata.Event {
	return []metadata.Event{
		{ID: "e1", Kind: metadata.KindSubtitle, StartNS: 2e9, EndNS: 4e9, Source: "srt"},
		{ID: "e2", Kind: metadata.KindSubtitle, StartNS: 6e9, EndNS: 8e9, Source: "srt"},
		{ID: "e3", Kind: metadata.KindSubtitle, StartNS: 8.5e9, EndNS: 9.5e9, Source: "srt"},
	}
}

func TestPlanAlignsToKeyframes(t *testing.T) {
	media := testMedia(t)
	budget := DefaultBudget()
	plan, err := BuildPlan(PlanOptions{Media: media, Events: testEvents(), Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Slices) != 3 {
		t.Fatalf("slices = %d", len(plan.Slices))
	}
	kf, e := index.BuildKeyframeIndex(media.VideoTrack())
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range plan.Slices {
		found := false
		for i := 0; i < kf.Len(); i++ {
			if kf.Frame(i).PTSNS() == s.AlignedStartNS {
				found = true
			}
		}
		if !found {
			t.Fatalf("slice %s start %d is not a keyframe", s.ID, s.AlignedStartNS)
		}
		if s.AlignedStartNS < s.WindowStartNS {
			t.Fatalf("slice %s starts before its window", s.ID)
		}
		if s.EventStartNS < s.AlignedStartNS || s.EventEndNS > s.AlignedEndNS {
			t.Fatalf("slice %s does not cover its event: %+v", s.ID, s)
		}
	}
}

func TestPlanHonoursPrePostWindow(t *testing.T) {
	media := testMedia(t)
	budget := DefaultBudget()
	budget.PreNS = 1_000_000_000
	budget.PostNS = 1_000_000_000
	plan, err := BuildPlan(PlanOptions{Media: media, Events: testEvents()[:1], Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	s := plan.Slices[0]
	if s.WindowStartNS != 1_000_000_000 || s.WindowEndNS != 5_000_000_000 {
		t.Fatalf("window = %d..%d", s.WindowStartNS, s.WindowEndNS)
	}
	// The fixture keyframes are every 40 frames (1.333 s), so with a 1 s
	// pre-roll the first keyframe inside the window is the good cut.
	if s.AlignedStartNS != 4_000_000_000/3 {
		t.Fatalf("aligned start = %d, want 1333333333", s.AlignedStartNS)
	}
	if s.AlignedStartNS < s.WindowStartNS || s.AlignedStartNS > s.EventStartNS {
		t.Fatalf("aligned start %d outside [window,event] = [%d,%d]", s.AlignedStartNS, s.WindowStartNS, s.EventStartNS)
	}
}

func TestPlanWindowClampedToMedia(t *testing.T) {
	media := testMedia(t)
	events := []metadata.Event{{ID: "edge", Kind: metadata.KindSubtitle, StartNS: 9.5e9, EndNS: 9.9e9}}
	plan, err := BuildPlan(PlanOptions{Media: media, Events: events, Budget: DefaultBudget()})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Slices[0].AlignedEndNS > media.DurationNS() {
		t.Fatalf("aligned end %d beyond media %d", plan.Slices[0].AlignedEndNS, media.DurationNS())
	}
}

func TestQualityGateRejectsLowWidth(t *testing.T) {
	media := testMedia(t)
	budget := DefaultBudget()
	budget.MinWidth = 1920
	_, e := BuildPlan(PlanOptions{Media: media, Events: testEvents(), Budget: budget})
	if e == nil || e.Code != timebase.CodeQualityGate {
		t.Fatalf("want QUALITY_GATE_FAILED, got %v", e)
	}
}

func TestQualityGateRejectsLowFPS(t *testing.T) {
	media := testMedia(t)
	budget := DefaultBudget()
	budget.MinFPS = 60
	_, e := BuildPlan(PlanOptions{Media: media, Events: testEvents(), Budget: budget})
	if e == nil || e.Code != timebase.CodeQualityGate {
		t.Fatalf("want QUALITY_GATE_FAILED, got %v", e)
	}
}

func TestRepresentationSelectionPrefersSmallest(t *testing.T) {
	budget := DefaultBudget()
	cands := []Candidate{
		{Name: "big", Width: 1920, Height: 1080, FPS: 30, VideoBytes: 9000},
		{Name: "small", Width: 1280, Height: 720, FPS: 30, VideoBytes: 3000},
		{Name: "tiny", Width: 640, Height: 360, FPS: 30, VideoBytes: 1000},
		{Name: "slow", Width: 1920, Height: 1080, FPS: 24, VideoBytes: 2000},
		{Name: "reencode", Width: 1920, Height: 1080, FPS: 60, VideoBytes: 100, Reencode: true},
	}
	passing, rejected, e := selectRepresentation(cands, budget, 1000)
	if e != nil {
		t.Fatal(e)
	}
	if len(passing) != 2 || passing[0].Name != "small" {
		t.Fatalf("passing = %+v", passing)
	}
	if len(rejected) != 3 {
		t.Fatalf("rejected = %+v", rejected)
	}
}

func TestBudgetExceededFailsClosed(t *testing.T) {
	media := testMedia(t)
	budget := DefaultBudget()
	budget.MaxWorkBytes = 1000
	_, e := BuildPlan(PlanOptions{Media: media, Events: testEvents(), Budget: budget})
	if e == nil || e.Code != timebase.CodeBudgetExceeded {
		t.Fatalf("want BUDGET_EXCEEDED, got %v", e)
	}
}

func TestAssetDigestReuse(t *testing.T) {
	media := testMedia(t)
	events := testEvents()
	plan, e := BuildPlan(PlanOptions{Media: media, Events: events, Budget: DefaultBudget()})
	if e != nil {
		t.Fatal(e)
	}
	known := map[string]string{}
	for _, s := range plan.Slices {
		known[s.ID] = s.AssetDigest(media)
	}
	// Change only the last event's text: exactly one slice must recompute.
	events[2].Text = "changed"
	plan2, e := BuildPlan(PlanOptions{Media: media, Events: events, Budget: DefaultBudget(), Known: known})
	if e != nil {
		t.Fatal(e)
	}
	if plan2.PlannedCount != 1 || plan2.ReusedCount != 2 {
		t.Fatalf("planned=%d reused=%d", plan2.PlannedCount, plan2.ReusedCount)
	}
	if plan2.Slices[2].Reused || !plan2.Slices[0].Reused || !plan2.Slices[1].Reused {
		t.Fatal("wrong slice was invalidated")
	}
}

func TestWriterCopiesSamplesAndRoundTrips(t *testing.T) {
	media := testMedia(t)
	plan, e := BuildPlan(PlanOptions{Media: media, Events: testEvents(), Budget: DefaultBudget()})
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "clips")
	w, e := NewWriter(media, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	for _, s := range plan.Slices {
		out, e := w.WriteSlice(s)
		if e != nil {
			t.Fatal(e)
		}
		parsed, err := index.ParseFile(out.Path, index.DefaultLimits())
		if err != nil {
			t.Fatalf("round trip %s: %v", s.ID, err)
		}
		if len(parsed.Tracks) != 2 {
			t.Fatalf("clip %s has %d tracks", s.ID, len(parsed.Tracks))
		}
		if parsed.VideoTrack().Width != fixtures.Width {
			t.Fatalf("clip %s lost video dimensions", s.ID)
		}
		// The cut starts on a keyframe, so the first video sample decodes.
		if !parsed.VideoTrack().Samples[0].Sync {
			t.Fatalf("clip %s does not start on a sync sample", s.ID)
		}
		if out.SHA256 == "" || out.Size == 0 {
			t.Fatalf("clip %s not hashed", s.ID)
		}
	}
}

func TestWriterRejectsOversizeOutput(t *testing.T) {
	media := testMedia(t)
	plan, e := BuildPlan(PlanOptions{Media: media, Events: testEvents(), Budget: DefaultBudget()})
	if e != nil {
		t.Fatal(e)
	}
	w, e := NewWriter(media, filepath.Join(t.TempDir(), "clips"))
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	w.MaxOutBytes = 64
	_, e = w.WriteSlice(plan.Slices[0])
	if e == nil || e.Code != timebase.CodeOutputTooLarge {
		t.Fatalf("want OUTPUT_TOO_LARGE, got %v", e)
	}
}

func TestEventDigestStableUnderOrder(t *testing.T) {
	a := testEvents()
	b := []metadata.Event{a[2], a[0], a[1]}
	if index.EventDigest(a) != index.EventDigest(b) {
		t.Fatal("event digest depends on enumeration order")
	}
}
