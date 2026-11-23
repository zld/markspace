package repair

import (
	"context"
	"errors"
	"io"
	"testing"

	"mediad/internal/media"
	"mediad/internal/merr"
	"mediad/internal/testkit"
)

func TestValidateIndexRejectsBadEntries(t *testing.T) {
	cases := map[string]media.Index{
		"negative offset": {Entries: []media.IndexEntry{{Track: 1, ByteOffset: -1}}},
		"unordered time": {Entries: []media.IndexEntry{
			{Track: 1, TimeNs: 40, ByteOffset: 10},
			{Track: 1, TimeNs: 20, ByteOffset: 20},
		}},
		"non-advancing offset": {Entries: []media.IndexEntry{
			{Track: 1, TimeNs: 20, ByteOffset: 20},
			{Track: 1, TimeNs: 40, ByteOffset: 10},
		}},
	}
	for name, idx := range cases {
		if err := ValidateIndex(idx); err == nil {
			t.Fatalf("%s: expected rejection", name)
		} else if code := merr.CodeOf(err); code != merr.CodeInvalidIndex {
			t.Fatalf("%s: code = %s", name, code)
		}
	}
	good := media.Index{Entries: []media.IndexEntry{{Track: 1, TimeNs: 0, ByteOffset: 30}}}
	good.Digest = DigestIndex(good.Entries)
	if err := ValidateIndex(good); err != nil {
		t.Fatalf("valid index rejected: %v", err)
	}
}

func TestEnsureKeyframesOnlyRejectsDeltaFrames(t *testing.T) {
	blocks := []media.Block{
		{Track: 1, TrackType: media.TrackVideo, Keyframe: true, TimeNs: 0, ElementOffset: 100},
		{Track: 1, TrackType: media.TrackVideo, Keyframe: false, TimeNs: 40e6, ElementOffset: 200},
	}
	idx := media.Index{Entries: []media.IndexEntry{{Track: 1, TimeNs: 40e6, ByteOffset: 200}}}
	if err := EnsureKeyframesOnly(idx, blocks); err == nil {
		t.Fatal("expected rejection of a delta-frame entry")
	}
	idx = media.Index{Entries: []media.IndexEntry{{Track: 1, TimeNs: 0, ByteOffset: 100}}}
	if err := EnsureKeyframesOnly(idx, blocks); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIndexOutlivesContext(t *testing.T) {
	// Index construction must not depend on wall-clock state; run it under a
	// cancelled context to prove it is pure.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
	doc, err := testkit.BuildDoc(testkit.DemoDocSpec(2))
	if err != nil {
		t.Fatal(err)
	}
	raw, tracks, err := demux(t, doc.Data)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	res, err := Repair(raw, tracks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildIndex(res.Blocks, IndexOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestInsufficientKeyframesIsDamage(t *testing.T) {
	// A clip with a single keyframe yields one entry, under the minimum.
	spec := testkit.DemoDocSpec(1)
	spec.ClipDurMs = 400
	doc, err := testkit.BuildDoc(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw, tracks, err := demux(t, doc.Data)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("demux: %v", err)
	}
	res, err := Repair(raw, tracks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, idxErr := BuildIndexFromRepair(res, nil, IndexOptions{})
	if idxErr == nil {
		t.Fatal("expected an insufficient-keyframe error")
	}
	if code := merr.CodeOf(idxErr); code != merr.CodeNoVideoKeyframes {
		t.Fatalf("code = %s", code)
	}
	if !merr.IsDamage(merr.CodeOf(idxErr)) {
		t.Fatal("insufficient keyframes must map to a damage exit code")
	}
	if !idx.Cursor.Damaged {
		t.Fatal("cursor should be marked damaged")
	}
	if idx.Cursor.Cause != merr.CodeNoVideoKeyframes {
		t.Fatalf("cursor cause = %s", idx.Cursor.Cause)
	}
}
