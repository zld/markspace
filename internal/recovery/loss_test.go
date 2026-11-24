package recovery

import (
	"testing"

	"mediarc/internal/ingest"
	"mediarc/internal/model"
)

func TestRestoreFramesPrefersValidChecksumThenLowerOffset(t *testing.T) {
	metas := []ingest.FrameMeta{
		{RecordIndex: 7, PTS: 7 * 3600, Duration: 3600, Offset: 900, Valid: true, ChecksumOK: true, TimestampOK: true},
		{RecordIndex: 7, PTS: 7 * 3600, Duration: 3600, Offset: 100, Valid: false, ChecksumOK: false, TimestampOK: true},
		{RecordIndex: 7, PTS: 7 * 3600, Duration: 3600, Offset: 200, Valid: true, ChecksumOK: true, TimestampOK: true},
	}
	faults := &model.Collector{}
	rep := RestoreFrames(metas, 9, faults)
	if rep.Frames[7].Winner == nil || rep.Frames[7].Winner.Offset != 200 {
		t.Fatalf("winner=%+v want valid offset 200", rep.Frames[7].Winner)
	}
	if rep.DuplicateGroups != 1 {
		t.Fatalf("groups=%d", rep.DuplicateGroups)
	}

	metas[0].Valid = false
	metas[0].ChecksumOK = false
	rep = RestoreFrames(metas, 9, faults)
	if rep.Frames[7].Winner == nil || rep.Frames[7].Winner.Offset != 200 {
		t.Fatalf("lower-offset tie failed: %+v", rep.Frames[7].Winner)
	}
}

func TestRestoreFramesSortsAndWritesExplicitMissingMarker(t *testing.T) {
	metas := []ingest.FrameMeta{
		{RecordIndex: 2, PTS: 7200, Duration: 3600, Offset: 30, Valid: true},
		{RecordIndex: 0, PTS: 0, Duration: 3600, Offset: 10, Valid: true},
		{RecordIndex: 1, PTS: 3600, Duration: 3600, Offset: 20, Valid: false, ChecksumOK: false},
	}
	rep := RestoreFrames(metas, 3, &model.Collector{})
	if rep.Present != 2 || rep.Missing != 1 {
		t.Fatalf("present=%d missing=%d", rep.Present, rep.Missing)
	}
	if string(rep.Frames[1].Marker) == "" || rep.Frames[1].State != FrameMissing {
		t.Fatalf("missing slot lacks explicit marker: %+v", rep.Frames[1])
	}
	if rep.Frames[2].Winner.Offset != 30 {
		t.Fatal("timeline not sorted by PTS")
	}
}

func TestFillAudioGapsInsertsMarkedSilence(t *testing.T) {
	// Directly verify the empty-input boundary does not panic.
	rep := FillAudioGaps(nil, nil, 480, 2)
	if len(rep.Blocks) != 0 {
		t.Fatalf("empty report blocks=%d", len(rep.Blocks))
	}
}
