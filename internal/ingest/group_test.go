package ingest

import (
	"encoding/binary"
	"errors"
	"testing"

	"mediad/internal/mp4"
	"mediad/internal/timeline"
)

// rawFragment parses one moof/mdat pair from a concatenated fixture.
func rawFragment(t *testing.T, data []byte) *mp4.Fragment {
	t.Helper()
	r := mp4.NewSeeker(data)
	boxes, err := mp4.ScanTopLevel(r, "g.m4s", mp4.Limits{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f, err := mp4.DecodeFragment(r, "g.m4s", boxes[0], boxes[1], mp4.Limits{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return f
}

// rawFragments decodes every moof/mdat pair in one shared buffer so their
// byte ranges and content digests are all relative to that buffer.
func rawFragments(t *testing.T, data []byte) []*mp4.Fragment {
	t.Helper()
	r := mp4.NewSeeker(data)
	boxes, err := mp4.ScanTopLevel(r, "g.m4s", mp4.Limits{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var frags []*mp4.Fragment
	for i := 0; i < len(boxes); i++ {
		if boxes[i].Type != "moof" {
			continue
		}
		var mdat *mp4.Box
		if i+1 < len(boxes) && boxes[i+1].Type == "mdat" {
			mdat = boxes[i+1]
		}
		f, err := mp4.DecodeFragment(r, "g.m4s", boxes[i], mdat, mp4.Limits{})
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		frags = append(frags, f)
	}
	return frags
}

func TestBuildGroupGapRecoverable(t *testing.T) {
	a := mp4.BuildTestFragment(1, 0, 4)
	b := mp4.BuildTestFragment(4, 0, 4)
	data := append(append([]byte{}, a...), b...)
	g, err := BuildGroup("grp", []*mp4.Fragment{rawFragment(t, a), rawFragment(t, b)}, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Gaps) != 2 || g.Gaps[0].MissingSeq != 2 || g.Gaps[1].MissingSeq != 3 {
		t.Fatalf("gaps wrong: %+v", g.Gaps)
	}
	if !g.HasGaps() || len(g.Fragments) != 2 {
		t.Fatalf("group shape wrong: %+v", g.Summary())
	}
}

func TestBuildGroupDuplicateMergesWhenEqual(t *testing.T) {
	a := mp4.BuildTestFragment(3, 0, 8)
	// Two identical copies behind a valid free box: same content at two
	// different absolute offsets inside one shared buffer.
	free := make([]byte, 16)
	binary.BigEndian.PutUint32(free[:4], 16)
	copy(free[4:8], "free")
	buf := append(append(free, a...), a...)
	frags := rawFragments(t, buf)
	if len(frags) != 2 {
		t.Fatalf("want 2 fragments, got %d", len(frags))
	}
	g, err := BuildGroup("grp", frags, buf)
	if err != nil {
		t.Fatalf("identical duplicate must merge: %v", err)
	}
	if g.Merged != 1 || len(g.Fragments) != 1 || g.Fragments[0].SeqNum != 3 {
		t.Fatalf("merge result wrong: %+v", g.Summary())
	}
	// Content digests must be offset- and buffer-independent.
	fStandalone := rawFragment(t, a)
	d1 := FragmentDigest(a, fStandalone)
	d2 := FragmentDigest(buf, frags[0])
	if d1 == "" || d1 != d2 {
		t.Fatalf("digests must match regardless of offset: %q vs %q", d1, d2)
	}
}

func TestBuildGroupConflictAborts(t *testing.T) {
	a := mp4.BuildTestFragment(5, 111, 4)
	b := mp4.BuildTestFragment(5, 222, 4) // same seq, different tfdt/content
	buf := append(a, b...)
	frags := rawFragments(t, buf)
	_, err := BuildGroup("grp", frags, buf)
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrSeqConflict {
		t.Fatalf("want E_SEQ_CONFLICT, got %v", err)
	}
	if me.Offset != frags[1].Moof.Offset {
		t.Fatalf("conflict must locate the second fragment at %d, got %d",
			frags[1].Moof.Offset, me.Offset)
	}
}

func TestBuildGroupSortsOutOfOrder(t *testing.T) {
	a := mp4.BuildTestFragment(2, 0, 4)
	b := mp4.BuildTestFragment(1, 0, 4)
	buf := append(a, b...)
	g, err := BuildGroup("grp", rawFragments(t, buf), buf)
	if err != nil {
		t.Fatal(err)
	}
	if g.Summary().Sequences[0] != 1 || g.Summary().Sequences[1] != 2 {
		t.Fatalf("not sorted: %+v", g.Summary().Sequences)
	}
}
