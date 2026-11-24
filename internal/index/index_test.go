package index

import (
	"bytes"
	"testing"

	"archive-recovery/internal/media"
)

func encodeLegacy(t *testing.T, streamSize int64, entries []Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	ri := RandomAccessIndex{StreamSize: streamSize, Entries: entries}
	if err := ri.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sampleFrames() []media.FrameEvent {
	return []media.FrameEvent{
		{Index: 0, Offset: 100, Length: 50, IsFrame: true, IsKeyframe: true},
		{Index: 1, Offset: 150, Length: 40, IsFrame: true, IsKeyframe: false},
		{Index: 2, Offset: 190, Length: 60, IsFrame: true, IsKeyframe: true},
		{Index: 3, Offset: 250, Length: 30, IsFrame: false, IsKeyframe: false}, // non-frame
	}
}

func TestReadLegacyIndexRoundTrip(t *testing.T) {
	entries := []Entry{
		{FrameSeq: 0, Offset: 100, Length: 50, IsKeyframe: true},
		{FrameSeq: 1, Offset: 150, Length: 40, IsKeyframe: false},
	}
	raw := encodeLegacy(t, 190, entries)
	got, err := ReadLegacyIndex(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != LegacyIndexVersion || got.StreamSize != 190 || len(got.Entries) != 2 {
		t.Fatalf("header parse wrong: %+v", got)
	}
	if !got.Entries[0].IsKeyframe || got.Entries[1].IsKeyframe {
		t.Fatal("keyframe flags lost in round trip")
	}
}

func TestReadLegacyIndexRejectsBadMagicAndVersion(t *testing.T) {
	raw := encodeLegacy(t, 10, nil)
	raw[0] = 'X'
	if _, err := ReadLegacyIndex(bytes.NewReader(raw)); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("bad magic must be invalid-argument, got %v", err)
	}
	raw = encodeLegacy(t, 10, nil)
	raw[8] = 99
	if _, err := ReadLegacyIndex(bytes.NewReader(raw)); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("unknown version must fail explicitly, got %v", err)
	}
}

func TestValidateDetectsBadOffsetAndFlag(t *testing.T) {
	frames := sampleFrames()
	good := []Entry{
		{FrameSeq: 0, Offset: 100, Length: 50, IsKeyframe: true},
		{FrameSeq: 1, Offset: 150, Length: 40, IsKeyframe: false},
		{FrameSeq: 2, Offset: 190, Length: 60, IsKeyframe: true},
	}
	// Corrupt: entry 1 points past EOF; entry 2 flips its keyframe flag.
	bad := []Entry{
		good[0],
		{FrameSeq: 1, Offset: 9000, Length: 40, IsKeyframe: false},
		{FrameSeq: 2, Offset: 190, Length: 60, IsKeyframe: false},
	}
	li := &LegacyIndex{Version: LegacyIndexVersion, StreamSize: 280, Entries: bad}
	st := Validate(li, frames, 280)
	if !st[0].Valid {
		t.Fatalf("entry 0 should validate: %+v", st[0])
	}
	if st[1].Valid || st[1].Reason != "offset_out_of_bounds" {
		t.Fatalf("out-of-bounds entry: %+v", st[1])
	}
	if st[2].Valid || st[2].Reason != "keyframe_flag_mismatch" {
		t.Fatalf("flag mismatch entry: %+v", st[2])
	}
}

func TestValidateRejectsNonFrameBoundary(t *testing.T) {
	frames := sampleFrames()
	li := &LegacyIndex{
		Version: LegacyIndexVersion, StreamSize: 280,
		Entries: []Entry{{FrameSeq: 0, Offset: 120, Length: 30, IsKeyframe: false}},
	}
	st := Validate(li, frames, 280)
	if st[0].Valid || st[0].Reason != "offset_not_a_frame_boundary" {
		t.Fatalf("interior offset must not validate: %+v", st[0])
	}
}

func TestRebuildReusesValidAndReplacesInvalid(t *testing.T) {
	frames := sampleFrames()
	legacy := []Entry{
		{FrameSeq: 0, Offset: 100, Length: 50, IsKeyframe: true},
		{FrameSeq: 1, Offset: 9000, Length: 40, IsKeyframe: false}, // invalid
		{FrameSeq: 2, Offset: 190, Length: 60, IsKeyframe: true},
	}
	li := &LegacyIndex{Version: LegacyIndexVersion, StreamSize: 280, Entries: legacy}
	statuses := Validate(li, frames, 280)
	idx := Rebuild(li, statuses, frames, 280)

	if len(idx.Entries) != 3 { // non-frame unit excluded
		t.Fatalf("want 3 frame entries, got %d", len(idx.Entries))
	}
	if idx.Entries[0] != legacy[0] {
		t.Fatal("valid legacy entry must be reused verbatim")
	}
	if idx.Entries[1].Offset != 150 || idx.Entries[1].Length != 40 {
		t.Fatalf("invalid region must be rebuilt from parser: %+v", idx.Entries[1])
	}
	for i, e := range idx.Entries {
		if e.FrameSeq != uint32(i) {
			t.Fatalf("rebuilt sequences must be dense, entry %d = %d", i, e.FrameSeq)
		}
	}
	keys := idx.Keyframes()
	if len(keys) != 2 || keys[0].Offset != 100 || keys[1].Offset != 190 {
		t.Fatalf("keyframe projection wrong: %+v", keys)
	}
}

func TestRebuildIsDeterministic(t *testing.T) {
	frames := sampleFrames()
	idx1 := Rebuild(nil, nil, frames, 280)
	idx2 := Rebuild(nil, nil, frames, 280)
	b1 := encodeLegacy(t, idx1.StreamSize, idx1.Entries)
	b2 := encodeLegacy(t, idx2.StreamSize, idx2.Entries)
	if !bytes.Equal(b1, b2) {
		t.Fatal("repeated rebuilds must serialize identically")
	}
}

func TestPartialEntryRejected(t *testing.T) {
	raw := encodeLegacy(t, 10, []Entry{{FrameSeq: 0, Offset: 1, Length: 1}})
	raw = append(raw, 1, 2, 3) // trailing partial record
	if _, err := ReadLegacyIndex(bytes.NewReader(raw)); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("partial entry must be rejected, got %v", err)
	}
}
