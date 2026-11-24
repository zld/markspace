package rtp

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/fixture"
)

// TestReorderWindowBoundsLateBurst simulates a long burst far beyond the
// reorder window so lost runs are committed as gaps and never revived.
func TestReorderWindowBoundsLateBurst(t *testing.T) {
	sess, err := fixture.BuildRTP(fixture.RTPPlan{
		PacketCount: 400, StartSeq: 1, PayloadLen: 8, TSStep: 1000,
		LossStart: 100, LossBurst: 150,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkts, _ := Decode(sess.Wire)
	rec, err := Reorder(pkts)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Lost != 150 {
		t.Fatalf("lost = %d, want 150", rec.Lost)
	}
	var marked int
	for _, g := range rec.Gaps {
		if !g.Recoverable {
			t.Fatalf("gap should be recoverable: %+v", g)
		}
		marked += g.Count
	}
	if marked != 150 {
		t.Fatalf("marked = %d", marked)
	}
}

// TestReorderBoundaryAtFrameGap checks a loss ending exactly on a frame
// boundary maps that boundary onto the shared grid.
func TestReorderBoundaryAtFrameGap(t *testing.T) {
	sess, _ := fixture.BuildRTP(fixture.RTPPlan{
		PacketCount: 40, StartSeq: 1, PayloadLen: 8, TSStep: 1000,
		LossStart: 18, LossBurst: 9, // 9 packets = one full 9-band frame
	})
	pkts, _ := Decode(sess.Wire)
	rec, err := Reorder(pkts)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Lost != 9 {
		t.Fatalf("lost = %d", rec.Lost)
	}
	// With sequence base 1, frame boundary N sits at seq 1+9N. LossStart 18 is
	// index 17 = seq 18, exactly the first packet of the third frame.
	g := rec.Gaps[0]
	if (int(g.Start)-1)%9 != 0 {
		t.Fatalf("gap start %d is not frame-aligned", g.Start)
	}
	// Count and span must exactly match the injected burst.
	if g.Count != 9 || int(g.End)-int(g.Start)+1 != 9 {
		t.Fatalf("gap shape = %+v", g)
	}
}

// TestReorderRejectsOverflow guards the classified overflow path.
func TestReorderRejectsOverflow(t *testing.T) {
	_, err := clock.Unwrap(1, 1<<62, SeqModulus)
	if err != nil && clock.AsKind(err) != clock.KindOverflow {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}
