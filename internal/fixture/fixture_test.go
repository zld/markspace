package fixture

import "testing"

func TestGenerateStreamDeterministic(t *testing.T) {
	c := DefaultConfig(42)
	c.JitterMS, c.LossPct = 80, 3
	a := GenerateStream(c)
	b := GenerateStream(c)
	if len(a.Packets) != len(b.Packets) {
		t.Fatalf("packet count differs: %d vs %d", len(a.Packets), len(b.Packets))
	}
	for i := range a.Packets {
		if a.Packets[i].Seq != b.Packets[i].Seq {
			t.Fatalf("non-deterministic stream at %d: %d vs %d", i, a.Packets[i].Seq, b.Packets[i].Seq)
		}
	}
}

func TestAllFaultsPresent(t *testing.T) {
	c := DefaultConfig(42)
	c.JitterMS, c.LossPct = 80, 3
	s := GenerateLossAcrossSubtitle(c)
	if len(s.Duplicates) != 1 || s.Duplicates[0] != c.FirstBadSeq {
		t.Errorf("duplicate anchor = %v, want [%d]", s.Duplicates, c.FirstBadSeq)
	}
	if len(s.Reordered) != 2 {
		t.Errorf("reorder pair = %v", s.Reordered)
	}
	// The protected contiguous-loss trio must all be absent.
	want := map[uint16]bool{SubtitleSyncSeq - 1: true, SubtitleSyncSeq: true, SubtitleSyncSeq + 1: true}
	dropped := map[uint16]bool{}
	for _, d := range s.Dropped {
		dropped[d] = true
	}
	for seq := range want {
		if !dropped[seq] {
			t.Errorf("forced contiguous loss missing seq %d", seq)
		}
	}
}

func TestJitterStaysBounded(t *testing.T) {
	c := DefaultConfig(7)
	c.JitterMS, c.LossPct = 32, 0
	s := GenerateStream(c)
	maxDisp := 0
	for pos, p := range s.Packets {
		d := pos - int(p.Seq)
		if d < 0 {
			d = -d
		}
		if d > maxDisp {
			maxDisp = d
		}
	}
	if maxDisp > 40 {
		t.Fatalf("jitter displacement %d exceeds bound", maxDisp)
	}
}

func TestCueText(t *testing.T) {
	srt, vtt := CueText()
	if srt == "" || vtt == "" || len(srt) < 20 || len(vtt) < 20 {
		t.Fatal("cue documents must be non-trivial")
	}
	if got := IllegalDFLabel(); got != "00:01:00:00" {
		t.Fatalf("illegal DF label = %q", got)
	}
}
