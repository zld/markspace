package derive

import (
	"testing"

	"mediacheck/internal/gap"
	"mediacheck/internal/timebase"
	"mediacheck/internal/track"
)

func newTestTrack(t *testing.T, frames int64) *track.Track {
	t.Helper()
	tr, err := track.New("cam", 48000, 2, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendChunk(0, make([]int32, frames*2), 44); err != nil {
		t.Fatal(err)
	}
	tr.TimelineEnd = frames
	return tr
}

func TestBuildFragmentIsFrameAlignedAndReadable(t *testing.T) {
	tr := newTestTrack(t, 64)
	seg, err := BuildFragment(tr, 0, 64, 44, 256, BuildOptions{OutputBits: 16, Sequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if seg.View.PayloadBytes != 256 || seg.View.EndTick-seg.View.StartTick == 0 {
		t.Fatalf("view=%+v", seg.View)
	}
	boxes, err := ReadBoxes(seg.Data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"styp": false, "moof": false, "mdat": false, "tfdt": false, "trun": false}
	for _, b := range boxes {
		if _, ok := want[b]; ok {
			want[b] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("box %s not found in %v", name, boxes)
		}
	}
	seg2, err := BuildFragment(tr, 0, 64, 44, 256, BuildOptions{OutputBits: 16, Sequence: 1})
	if err != nil || seg.SHA256 != seg2.SHA256 {
		t.Fatal("fragment digest not deterministic")
	}
}

func TestPlanSplitsContiguousRunsOnly(t *testing.T) {
	tr, err := track.New("cam", 48000, 1, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = tr.AppendChunk(0, make([]int32, 10), 44)
	_ = tr.AppendChunk(20, make([]int32, 10), 64)
	g, _ := gap.NewLedger("cam", 48000).RecordMissing(1, 1, 1, 10, 20, "loss")
	tr.AddGaps([]gap.Gap{g})
	tr.TimelineEnd = 30
	plans, err := PlanSegments(tr, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 4 {
		t.Fatalf("plans=%+v want two runs split into four", plans)
	}
	for _, p := range plans {
		if p.FrameCount != 5 {
			t.Fatalf("unexpected plan %+v", p)
		}
	}
}

func TestCorruptOrGappedRangeDoesNotBuild(t *testing.T) {
	tr := newTestTrack(t, 48)
	if _, err := BuildFragment(tr, 0, 49, 44, 196, BuildOptions{OutputBits: 16}); err == nil {
		t.Fatal("absent frame built")
	}
	g, _ := gap.NewLedger("cam", 48000).RecordMissing(1, 1, 1, 24, 48, "loss")
	tr.AddGaps([]gap.Gap{g})
	tr.TimelineEnd = 48
	if _, err := BuildFragment(tr, 0, 48, 44, 192, BuildOptions{OutputBits: 16}); !timebase.IsCode(err, timebase.CodeFragment) {
		t.Fatalf("err=%v want fragment_invalid", err)
	}
}
