package fixture

import (
	"bytes"
	"testing"

	"mediatool/internal/mp4"
	"mediatool/internal/rtp"
	"mediatool/internal/timeline"
)

func TestGenerateDeterministic(t *testing.T) {
	a := Generate(42, 6, -1)
	b := Generate(42, 6, -1)
	if !bytes.Equal(a.Init, b.Init) {
		t.Fatal("init segment must be deterministic for a fixed seed")
	}
	if len(a.VideoFrags) != 6 || len(a.AudioFrags) != 6 {
		t.Fatalf("frag counts wrong: v=%d a=%d", len(a.VideoFrags), len(a.AudioFrags))
	}
	for i := range a.VideoFrags {
		if !bytes.Equal(a.VideoFrags[i].Bytes, b.VideoFrags[i].Bytes) {
			t.Fatalf("video fragment %d differs across runs", i)
		}
	}
}

func TestGeneratedFragmentsParse(t *testing.T) {
	g := Generate(7, 4, -1)
	tracks, err := mp4.ParseInit(bytes.NewReader(g.Init))
	if err != nil || len(tracks) != 2 {
		t.Fatalf("init parse failed: %v tracks=%d", err, len(tracks))
	}
	for _, f := range g.VideoFrags {
		frag, perr := mp4.ParseFragment(bytes.NewReader(f.Bytes), 0)
		if perr != nil || frag.Rejected {
			t.Fatalf("video frag %d invalid: %v %+v", f.Seq, perr, frag.Errors)
		}
		if frag.Seq != f.Seq || frag.Runs[0].BaseMediaDecode != f.DTS {
			t.Fatalf("frag %d header mismatch", f.Seq)
		}
		// Payload bytes must be preserved.
		if !bytes.Equal(frag.MdatPayload, f.Payload) {
			t.Fatalf("frag %d payload mismatch", f.Seq)
		}
	}
}

func TestVariableFrameRatePreserved(t *testing.T) {
	g := Generate(1, 10, -1)
	seenDifferent := false
	nominal := uint32(3003)
	for _, f := range g.VideoFrags {
		for _, d := range f.Durations {
			if d != nominal {
				seenDifferent = true
			}
			if d == 0 {
				t.Fatal("VFR duration must never be zero")
			}
		}
	}
	if !seenDifferent {
		t.Fatal("video generator must produce variable sample durations")
	}
	// Audio durations stay constant.
	for _, f := range g.AudioFrags {
		for _, d := range f.Durations {
			if d != 1024 {
				t.Fatalf("audio duration must be fixed, got %d", d)
			}
		}
	}
}

func TestCorruptCasesRejected(t *testing.T) {
	g := Generate(1, 2, -1)
	if len(g.Corrupted) == 0 {
		t.Fatal("corrupt fixtures must not be empty")
	}
	for _, c := range g.Corrupted {
		_, err := mp4.ParseFragment(bytes.NewReader(c.Bytes), 0)
		me := timeline.AsMediaError(err)
		if me == nil {
			t.Fatalf("case %s must be rejected", c.Name)
		}
		if string(me.Class) != c.Class {
			t.Fatalf("case %s: class=%s want %s", c.Name, me.Class, c.Class)
		}
	}
}

func TestRTPWraparoundGenerated(t *testing.T) {
	g := Generate(3, 6, -1)
	if len(g.RTPVideo) != 6 || len(g.RTPEvents) != 6 {
		t.Fatal("RTP fixtures missing")
	}
	u := &rtp.Unwrapper{}
	var last uint64
	for i, pkt := range g.RTPVideo {
		buf, err := pkt.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		_, raw, err := rtp.ParsePacket(buf)
		if err != nil {
			t.Fatal(err)
		}
		got, uerr := u.Unwrap(raw)
		if uerr != nil {
			t.Fatalf("packet %d unexpectedly rejected: %v", i, uerr)
		}
		if i > 0 && got < last {
			t.Fatalf("unwrapped stream regressed at %d", i)
		}
		last = got
	}
	if u.WrapCount() == 0 {
		t.Fatal("generated RTP stream must cross the 32-bit boundary")
	}
}

func TestReorderLate(t *testing.T) {
	g := Generate(5, 5, 3)
	ordered := ReorderLate(g.VideoFrags, g.LateAt)
	if len(ordered) != len(g.VideoFrags) {
		t.Fatal("reorder must preserve fragment count")
	}
	// The fragment originally at index 3 must now be last.
	if ordered[len(ordered)-1].Seq != g.VideoFrags[3].Seq {
		t.Fatal("late fragment must be moved to the end")
	}
	// Original slice is untouched.
	if g.VideoFrags[3].Seq != 3 {
		t.Fatal("ReorderLate must not mutate its input")
	}
}

func TestRollbackTfdtStructurallyValid(t *testing.T) {
	g := Generate(9, 2, -1)
	rolled := RollbackTfdt(g.VideoFrags[1], 100)
	frag, err := mp4.ParseFragment(bytes.NewReader(rolled), 0)
	if err != nil || frag.Rejected {
		t.Fatalf("rolled-back fragment must stay structurally valid: %v", err)
	}
	if frag.Runs[0].BaseMediaDecode != 100 {
		t.Fatalf("tfdt rollback not applied: %d", frag.Runs[0].BaseMediaDecode)
	}
}
