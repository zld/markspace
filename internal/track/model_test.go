package track

import "testing"

func TestCueValidate(t *testing.T) {
	ok := Cue{ID: "a#1", Start: 10, End: 20}
	if err := ok.Validate(); err != nil {
		t.Fatalf("normal cue rejected: %v", err)
	}
	bad := Cue{ID: "a#2", Start: 20, End: 10}
	if err := bad.Validate(); err == nil {
		t.Fatal("reversed interval must fail validation")
	}
	zero := Cue{ID: "a#3", Start: 5, End: 5}
	if err := zero.Validate(); err != nil {
		t.Fatalf("zero-length interval is allowed: %v", err)
	}
}

func TestTimelineCues(t *testing.T) {
	c1 := &Cue{ID: "a#1"}
	c2 := &Cue{ID: "b#1"}
	tl := Timeline{Entries: []Entry{
		{Kind: KindCue, Cue: c1},
		{Kind: KindGap, Start: 1, End: 2},
		{Kind: KindCue, Cue: c2},
		{Kind: KindRej, Start: 3, End: 4},
	}}
	got := tl.Cues()
	if len(got) != 2 || got[0] != c1 || got[1] != c2 {
		t.Fatalf("Cues() = %v, want both cues in order", got)
	}
}
