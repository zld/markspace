package segment_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/segment"
	"github.com/zld/mediarc/internal/timeline"
)

func testConfig() model.Config {
	cfg := model.DefaultConfig()
	cfg.ReferenceTrackID = 1
	cfg.SegmentDurationNS = 2 * model.Second
	cfg.BoundaryToleranceNS = 20 * model.Millisecond
	cfg.Anchors = nil
	return cfg
}

func event(track, sample uint32, order int, t model.NS, key bool) model.RawEvent {
	return model.RawEvent{
		TrackID: track, SampleNumber: sample, OrderIndex: order,
		TimestampBits: 64, Timescale: uint32(model.Second),
		RawTimestamp: uint64(t), Keyframe: key,
		Payload: []byte(fmt.Sprintf("t%d-s%d", track, sample)),
	}
}

func alignedEvents() []model.RawEvent {
	var raw []model.RawEvent
	times := []model.NS{0, model.Second, 2 * model.Second, 3 * model.Second, 4 * model.Second}
	keys := map[int]bool{0: true, 2: true, 4: true}
	for track := uint32(1); track <= 2; track++ {
		for i, t := range times {
			raw = append(raw, event(track, uint32(i+1), i, t, keys[i]))
		}
	}
	return raw
}

func TestCutAlignsRepresentationsAndHashes(t *testing.T) {
	cfg := testConfig()
	built, err := timeline.Build(alignedEvents(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	metas := []model.TrackMetadata{
		{TrackID: 1, Representation: "high", EventCount: 5},
		{TrackID: 2, Representation: "low", EventCount: 5},
		{TrackID: 3, Representation: "empty", EventCount: 0, Empty: true},
	}
	report, err := segment.Cut(built, metas, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Segments) != 2 {
		t.Fatalf("segments=%d want 2: %+v", len(report.Segments), report.Segments)
	}
	if report.KeyframeHits != report.KeyframeChecks {
		t.Fatalf("keyframe hits=%d checks=%d", report.KeyframeHits, report.KeyframeChecks)
	}
	for _, seg := range report.Segments {
		if len(seg.Representations) != 2 {
			t.Fatalf("representations=%d", len(seg.Representations))
		}
		if seg.SHA256 == "" {
			t.Fatal("empty segment hash")
		}
		for _, rep := range seg.Representations {
			if rep.SHA256 == "" || rep.ChunkCount == 0 {
				t.Fatalf("bad rep hash/chunks: %+v", rep)
			}
		}
	}
	foundEmptyIssue := false
	for _, issue := range report.Issues {
		if issue.Code == "empty_track" && issue.TrackID == 3 {
			foundEmptyIssue = true
		}
	}
	if !foundEmptyIssue {
		t.Fatalf("missing empty track issue: %+v", report.Issues)
	}
}

func TestCutReportsMisalignedRepresentation(t *testing.T) {
	cfg := testConfig()
	raw := alignedEvents()
	for i := range raw {
		if raw[i].TrackID == 2 && raw[i].SampleNumber == 3 {
			raw[i].RawTimestamp = uint64(2*model.Second + 100*model.Millisecond)
		}
	}
	built, err := timeline.Build(raw, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = segment.Cut(built, []model.TrackMetadata{
		{TrackID: 1, EventCount: 5},
		{TrackID: 2, EventCount: 5},
	}, cfg, nil)
	var align *model.AlignmentError
	if !errors.As(err, &align) {
		t.Fatalf("err=%v want AlignmentError", err)
	}
	if align.TrackID != 2 || align.DeviationNS.Abs() <= cfg.BoundaryToleranceNS {
		t.Fatalf("unexpected alignment error: %+v", align)
	}
}
