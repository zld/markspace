package check

import (
	"testing"
	"time"

	"subsync/internal/model"
)

func mk(id, text string, start, end time.Duration) model.Cue {
	return model.Cue{ID: id, Track: "default", Text: text, Start: start, End: end}
}

func TestVerifyAllMatched(t *testing.T) {
	orig := []model.Cue{
		mk("1", "hello", 0, time.Second),
		mk("2", "world", 2*time.Second, 3*time.Second),
	}
	back := []model.Cue{
		mk("1", "hello", time.Millisecond, time.Second+2*time.Millisecond),
		mk("2", "world", 2*time.Second, 3*time.Second),
	}
	rep := Verify(orig, back)
	if rep.Missing != 0 || rep.TextChange != 0 || rep.Duplicates != 0 {
		t.Errorf("unexpected issues: %+v", rep)
	}
	if rep.MaxDelta != 2*time.Millisecond {
		t.Errorf("max delta = %s, want 2ms", rep.MaxDelta)
	}
	for _, cr := range rep.PerCue {
		if !cr.Matched {
			t.Errorf("cue %q not matched", cr.Cue.ID)
		}
	}
}

func TestVerifyMissing(t *testing.T) {
	orig := []model.Cue{mk("1", "a", 0, time.Second), mk("2", "b", 0, time.Second)}
	back := []model.Cue{mk("1", "a", 0, time.Second)}
	rep := Verify(orig, back)
	if rep.Missing != 1 {
		t.Errorf("missing = %d, want 1", rep.Missing)
	}
	if rep.PerCue[1].Issue != IssueMissing {
		t.Errorf("issue = %q, want missing", rep.PerCue[1].Issue)
	}
}

func TestVerifyDuplicate(t *testing.T) {
	orig := []model.Cue{mk("1", "a", 0, time.Second)}
	back := []model.Cue{mk("1", "a", 0, time.Second), mk("1", "a", 0, time.Second)}
	rep := Verify(orig, back)
	if rep.Duplicates != 1 || !rep.PerCue[0].Duplicate {
		t.Errorf("duplicate not detected: %+v", rep.PerCue[0])
	}
}

func TestVerifyTextChange(t *testing.T) {
	orig := []model.Cue{mk("1", "alpha", 0, time.Second)}
	back := []model.Cue{mk("1", "beta", 0, time.Second)}
	rep := Verify(orig, back)
	if rep.TextChange != 1 || rep.PerCue[0].Issue != IssueText {
		t.Errorf("text change not detected: %+v", rep)
	}
}

func TestVerifyFallbackByTextWithDuplicates(t *testing.T) {
	// No stable ids: two identical texts must map to two distinct outputs.
	orig := []model.Cue{
		{Track: "t", Text: "same", Start: 0, End: time.Second},
		{Track: "t", Text: "same", Start: 2 * time.Second, End: 3 * time.Second},
	}
	back := []model.Cue{
		{Track: "t", Text: "same", Start: 0, End: time.Second},
		{Track: "t", Text: "same", Start: 2 * time.Second, End: 3 * time.Second},
	}
	rep := Verify(orig, back)
	if rep.Missing != 0 {
		t.Errorf("text fallback failed: %+v", rep.PerCue)
	}
}

func TestNormalizeText(t *testing.T) {
	if NormalizeText("  a\r\nb\r\nc  ") != "a\nb\nc" {
		t.Error("normalization did not canonicalize CRLF and trim")
	}
}
