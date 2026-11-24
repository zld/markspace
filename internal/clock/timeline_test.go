package clock

import "testing"

func TestTimeline_PCROriginAndWrap(t *testing.T) {
	tl := NewTimeline()
	// Anchor the origin close to the 33-bit boundary so wrap needs small steps.
	if ms, at, amb := tl.FeedPCR(Mod33 - 90000); amb || ms != 0 || at != 0 {
		t.Fatalf("origin feed: ms=%d at=%d amb=%v", ms, at, amb)
	}
	_, before, amb := tl.FeedPCR(Mod33 - 1)
	if amb {
		t.Fatalf("pre-wrap feed ambiguous: at=%d", before)
	}
	// PCR value wraps from Mod33-1 to 0: unwrapped time must keep advancing.
	ms, after, amb := tl.FeedPCR(0)
	if amb || after <= before {
		t.Fatalf("PCR wrap moved backwards: before=%d after=%d amb=%v", before, after, amb)
	}
	if ms != 1000 || after != 90000 {
		t.Fatalf("post-wrap position: ms=%d at90k=%d", ms, after)
	}
}

func TestTimeline_AmbiguousPCR(t *testing.T) {
	tl := NewTimeline()
	tl.FeedPCR(0)
	if _, _, amb := tl.FeedPCR(uint64(HalfMod33) + 10); !amb {
		t.Fatal("expected ambiguous PCR event")
	}
}

func TestTimeline_PTSPerPIDAndEvents(t *testing.T) {
	tl := NewTimeline()
	tl.FeedPCR(90000) // origin = 1 s of ticks
	ms, _, amb := tl.FeedPTS(0x100, 90000+9000)
	if amb || ms != 100 {
		t.Fatalf("PTS mapping: ms=%d amb=%v", ms, amb)
	}
	// Same raw value on a different PID is an independent stream.
	ms2, _, amb := tl.FeedPTS(0x200, 90000+18000)
	if amb || ms2 != 200 {
		t.Fatalf("second PID PTS: ms=%d", ms2)
	}
	if pids := tl.PIDs(); len(pids) != 2 || pids[0] != 0x100 {
		t.Fatalf("PIDs = %v", pids)
	}
}

func TestRecorder(t *testing.T) {
	r := Recorder{}
	r.Add(Event{Kind: "cc_gap", Severity: "warning", PID: 0x100,
		Range: ByteRange{Start: 188, End: 376}, Detail: "gap of 2"})
	got := r.Events()
	if len(got) != 1 || got[0].At90k != -1 || r.CountByKind()["cc_gap"] != 1 {
		t.Fatalf("recorder state wrong: %+v", got)
	}
	if s := got[0].Describe(); s == "" {
		t.Fatal("Describe returned empty string")
	}
}
