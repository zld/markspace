package report_test

import (
	"testing"

	"mediasync/internal/config"
	"mediasync/internal/model"
	"mediasync/internal/report"
)

func timelineWith(t *testing.T, id string, classes ...model.EntryClass) *model.TrackTimeline {
	t.Helper()
	tl := &model.TrackTimeline{TrackID: id}
	for i, c := range classes {
		e := model.TimelineEntry{
			Class: c, SourceIndex: i, ExtSeq: uint64(i),
			Start: model.NS(i) * 10, Duration: 10,
		}
		if c == model.EntryGap {
			e.SourceIndex = -1
		}
		tl.Entries = append(tl.Entries, e)
	}
	return tl
}

func TestBuildTrackReportDigestStable(t *testing.T) {
	tl := timelineWith(t, "x", model.EntrySample, model.EntryDuplicate, model.EntryGap)
	a := report.BuildTrackReport(tl, nil, false)
	b := report.BuildTrackReport(tl, nil, false)
	if a.Digest != b.Digest || a.Digest == "" {
		t.Fatal("digest must be stable and non-empty")
	}
	if a.Status != report.StatusOK {
		t.Fatalf("status=%s", a.Status)
	}
}

func TestClassifyStatuses(t *testing.T) {
	tl := timelineWith(t, "x", model.EntrySample)
	tl.Failures = []model.Failure{model.NewFailure("x", model.StageOpus, 0, model.ErrEmptyPacket, "x")}
	if got := report.BuildTrackReport(tl, nil, false).Status; got != report.StatusFailed {
		t.Fatalf("failed status=%s", got)
	}
	if got := report.BuildTrackReport(tl, nil, true).Status; got != report.StatusBlocked {
		t.Fatalf("blocked status=%s", got)
	}

	clean := timelineWith(t, "y", model.EntrySample)
	ineligible := &config.Decision{
		TrackID: "y", Eligible: false,
		Unsatisfiable: []model.Failure{
			model.NewTrackFailure("y", model.StageConfig, model.ErrBudgetExceeded, "x"),
		},
	}
	if got := report.BuildTrackReport(clean, ineligible, false).Status; got != report.StatusRejected {
		t.Fatalf("rejected status=%s", got)
	}
}

func TestManifestSummaryAndDigest(t *testing.T) {
	t1 := timelineWith(t, "a", model.EntrySample, model.EntrySample, model.EntryDuplicate)
	t2 := timelineWith(t, "b", model.EntrySample, model.EntryGap)
	r1 := report.BuildTrackReport(t1, nil, false)
	r2 := report.BuildTrackReport(t2, nil, false)

	params := report.Params{BudgetNS: 100, MaxBitrate: 96000}
	m := report.Build(17, params, []report.TrackReport{r1, r2})
	if m.Summary.TrackCount != 2 || m.Summary.OKTracks != 2 {
		t.Fatalf("summary=%+v", m.Summary)
	}
	if m.Summary.SampleItems != 3 || m.Summary.GapItems != 1 || m.Summary.DuplicateItems != 1 {
		t.Fatalf("item counts wrong: %+v", m.Summary)
	}
	if m.Digest == "" {
		t.Fatal("manifest digest empty")
	}
	// Rerun with identical inputs must match.
	again := report.Build(17, params, []report.TrackReport{
		report.BuildTrackReport(t1, nil, false),
		report.BuildTrackReport(t2, nil, false),
	})
	if again.Digest != m.Digest {
		t.Fatal("manifest digest not stable across reruns")
	}
	// A changed seed must change the manifest digest.
	diffSeed := report.Build(18, params, []report.TrackReport{r1, r2})
	if diffSeed.Digest == m.Digest {
		t.Fatal("seed change not reflected in digest")
	}
}

func TestMappingTraceability(t *testing.T) {
	tl := timelineWith(t, "a", model.EntrySample, model.EntrySample)
	r := report.BuildTrackReport(tl, nil, false)
	for slot, mp := range r.Mappings {
		if mp.OutputSlot != slot || mp.SourceOffset != slot || mp.TrackID != "a" {
			t.Fatalf("mapping traceability broken: %+v", mp)
		}
	}
}
