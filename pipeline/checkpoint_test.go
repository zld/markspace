package pipeline

import (
	"strings"
	"testing"

	"mediamux/media"
)

func newCP() *Checkpoint {
	return NewCheckpoint("hls", 7, "md-1", "in-1", 7)
}

func TestCommitAdvancesCursorMonotonically(t *testing.T) {
	cp := newCP()
	if err := cp.CommitSegment(0, "d0", 100); err != nil {
		t.Fatal(err)
	}
	if err := cp.CommitSegment(1, "d1", 200); err != nil {
		t.Fatal(err)
	}
	if cp.Cursor != 2 || cp.LastCompleteIdx != 1 || cp.CommittedBytes != 300 {
		t.Fatalf("cp=%+v", cp)
	}
	if err := cp.CommitSegment(4, "d4", 1); err == nil {
		t.Fatal("out-of-order commit must fail")
	} else if !media.HasCode(err, media.CodeCursorBack) {
		t.Fatalf("err=%v", err)
	}
	if cp.Cursor != 2 {
		t.Fatalf("failed commit moved cursor: %d", cp.Cursor)
	}
}

func TestCommitIsIdempotentForSameDigest(t *testing.T) {
	cp := newCP()
	_ = cp.CommitSegment(0, "d0", 10)
	// Re-running the last segment after a crash must not double count if the
	// cursor is rewound deliberately to the last complete segment.
	cp.Cursor, cp.LastCompleteIdx, cp.CommittedBytes = 0, -1, 0
	if err := cp.CommitSegment(0, "d0", 10); err != nil {
		t.Fatal(err)
	}
	if cp.SegmentDigests[0] != "d0" {
		t.Fatalf("digest=%q", cp.SegmentDigests[0])
	}
}

func TestCommitRejectsChangedDigest(t *testing.T) {
	cp := newCP()
	_ = cp.CommitSegment(0, "d0", 10)
	cp.Cursor, cp.LastCompleteIdx = 0, -1
	err := cp.CommitSegment(0, "different", 10)
	if err == nil || !media.HasCode(err, media.CodeCachePoison) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateResumeAcceptsLastComplete(t *testing.T) {
	cp := newCP()
	_ = cp.CommitSegment(0, "d0", 10)
	_ = cp.CommitSegment(1, "d1", 10)
	dec, err := ValidateResume(cp, ResumeRequest{ManifestDigest: "md-1", InputDigest: "in-1", MaxSegments: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Resumed || dec.FromSegment != 2 {
		t.Fatalf("decision=%+v", dec)
	}
	if !strings.Contains(dec.Reason, "last complete segment 1") {
		t.Fatalf("reason=%q", dec.Reason)
	}
}

func TestValidateResumeRejectsManifestVersionChange(t *testing.T) {
	cp := newCP()
	_ = cp.CommitSegment(0, "d0", 10)
	_, err := ValidateResume(cp, ResumeRequest{
		ManifestDigest: "md-1", InputDigest: "in-1", MaxSegments: 5, ManifestVersion: 6,
	})
	if err == nil || !media.HasCode(err, media.CodeManifestChg) {
		t.Fatalf("err=%v", err)
	}
	// A checkpoint that recorded no version stays compatible.
	cp.ManifestVersion = 0
	if _, err := ValidateResume(cp, ResumeRequest{
		ManifestDigest: "md-1", InputDigest: "in-1", MaxSegments: 5, ManifestVersion: 6,
	}); err != nil {
		t.Fatalf("legacy checkpoint rejected: %v", err)
	}
}

func TestValidateResumeRejectsManifestChange(t *testing.T) {
	cp := newCP()
	_ = cp.CommitSegment(0, "d0", 10)
	_, err := ValidateResume(cp, ResumeRequest{ManifestDigest: "md-2", InputDigest: "in-1", MaxSegments: 5})
	if err == nil || !media.HasCode(err, media.CodeManifestChg) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateResumeRejectsInputChange(t *testing.T) {
	cp := newCP()
	_ = cp.CommitSegment(0, "d0", 10)
	_, err := ValidateResume(cp, ResumeRequest{ManifestDigest: "md-1", InputDigest: "in-2", MaxSegments: 5})
	if err == nil || !media.HasCode(err, media.CodeManifestChg) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateResumeRejectsCursorPastEnd(t *testing.T) {
	cp := newCP()
	for i := 0; i < 4; i++ {
		_ = cp.CommitSegment(i, "d", 1)
	}
	_, err := ValidateResume(cp, ResumeRequest{ManifestDigest: "md-1", InputDigest: "in-1", MaxSegments: 2})
	if err == nil || !media.HasCode(err, media.CodeCursorBack) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateResumeRejectsNonContiguousDigests(t *testing.T) {
	cp := newCP()
	cp.Cursor, cp.LastCompleteIdx = 2, 1
	cp.SegmentDigests = map[int]string{0: "a", 2: "c"}
	_, err := ValidateResume(cp, ResumeRequest{ManifestDigest: "md-1", InputDigest: "in-1", MaxSegments: 5})
	if err == nil || !media.HasCode(err, media.CodeCursorBack) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckpointSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nested/checkpoint.json"
	cp := newCP()
	_ = cp.CommitSegment(0, "d0", 11)
	cp.SetStage("parse", StageComplete)
	if err := cp.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Cursor != 1 || got.SegmentDigests[0] != "d0" || got.Stage("parse") != StageComplete {
		t.Fatalf("got=%+v", got)
	}
	if got.Stage("missing") != StagePending {
		t.Fatal("unknown stage must default to pending")
	}
}

func TestLoadMissingCheckpointIsFreshRun(t *testing.T) {
	cp, err := Load(t.TempDir() + "/nope.json")
	if err != nil || cp != nil {
		t.Fatalf("cp=%+v err=%v", cp, err)
	}
}

func TestValidateResumeNoCheckpointStartsAtZero(t *testing.T) {
	dec, err := ValidateResume(nil, ResumeRequest{ManifestDigest: "x", MaxSegments: 3})
	if err != nil || dec.Resumed || dec.FromSegment != 0 {
		t.Fatalf("dec=%+v err=%v", dec, err)
	}
}
