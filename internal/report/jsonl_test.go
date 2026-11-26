package report

import (
	"strings"
	"testing"

	"mediarc/internal/model"
)

func TestFinalizeSortsAndSummarizes(t *testing.T) {
	r := &Report{
		Errors: []ErrorEntry{
			{Stage: "index", Code: model.ErrMisaligned, Track: 1, Offset: 50},
			{Stage: "scan", Code: model.ErrTruncated, Track: 1, Offset: 41},
		},
		Events: []Event{
			{Track: 2, Offset: 10, Kind: model.ErrWrap},
			{Track: 1, Offset: 20, Kind: model.ErrSourceSwitch},
		},
	}
	r.Finalize()
	if r.Summary.FirstError != 41 {
		t.Fatalf("first_error = %d, want 41", r.Summary.FirstError)
	}
	if r.Errors[0].Offset != 41 || r.Events[0].Track != 1 {
		t.Fatalf("not sorted: %+v %+v", r.Errors, r.Events)
	}
}

func TestWriteDeterministic(t *testing.T) {
	build := func() string {
		r := &Report{Summary: Summary{Mapped: 6, FirstError: -1}}
		r.Finalize()
		var sb strings.Builder
		if err := Write(&sb, r); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	a, b := build(), build()
	if a != b {
		t.Fatal("output not deterministic")
	}
	if !strings.Contains(a, `"mapped": 6`) || !strings.HasSuffix(a, "\n") {
		t.Fatalf("unexpected output: %q", a)
	}
	if !strings.Contains(a, `"errors": []`) {
		t.Fatal("nil sections must render as empty arrays")
	}
}
