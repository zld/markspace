package report

import (
	"bytes"
	"strings"
	"testing"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

func sampleIssues() fault.List {
	return fault.List{
		{Code: fault.CodeSegmentHash, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "hash mismatch",
			Source: fault.Source{Manifest: "hls/a.m3u8", Representation: "a", Segment: "s2",
				Loc:   fault.Locator{File: "hls/a.m3u8", Line: 12, Offset: 300},
				Start: clock.MustNew(4, 1), End: clock.MustNew(8, 1)}},
		{Code: fault.CodeMissingDuration, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "no EXTINF",
			Source: fault.Source{Manifest: "dash/m.mpd", Representation: "v",
				Loc:   fault.Locator{File: "dash/m.mpd", Offset: 42},
				Start: clock.Zero, End: clock.MustNew(4, 1)}},
	}
}

func TestBuildSortsAndDigests(t *testing.T) {
	r := Build(sampleIssues(), Counts{Segments: 2}, nil)
	if r.Entries[0].Start.Cmp(r.Entries[1].Start) > 0 {
		t.Fatal("not sorted by start time")
	}
	if r.SortedViolations() != 0 || r.MissingSource() != 0 {
		t.Fatalf("verify=%+v", r.Summarise())
	}
	again := Build(sampleIssues(), Counts{Segments: 2}, nil)
	if r.Digest != again.Digest {
		t.Fatal("digest must be stable across builds")
	}

	var text bytes.Buffer
	if err := r.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "report-digest=") {
		t.Fatalf("text=%s", text.String())
	}
	var js bytes.Buffer
	if err := r.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), `"digest"`) {
		t.Fatal("json missing digest")
	}
}

func TestFilterByRange(t *testing.T) {
	r := Build(sampleIssues(), Counts{}, nil)
	from := clock.MustNew(4, 1)
	r.Filter(&from, nil)
	if len(r.Entries) != 1 || r.Entries[0].Code != fault.CodeSegmentHash {
		t.Fatalf("entries=%+v", r.Entries)
	}
	to := clock.MustNew(1, 1)
	r2 := Build(sampleIssues(), Counts{}, nil)
	r2.Filter(nil, &to)
	if len(r2.Entries) != 1 || r2.Entries[0].Code != fault.CodeMissingDuration {
		t.Fatalf("entries=%+v", r2.Entries)
	}
}

func TestMissingSourceDetected(t *testing.T) {
	r := Build(fault.List{{Code: fault.CodeParse, Severity: fault.SeverityError}}, Counts{}, nil)
	if r.MissingSource() != 1 {
		t.Fatalf("missing source=%d", r.MissingSource())
	}
	unsorted := &Report{Entries: []Entry{
		{Start: clock.MustNew(8, 1)}, {Start: clock.MustNew(4, 1)},
	}}
	if unsorted.SortedViolations() != 1 {
		t.Fatalf("violations=%d", unsorted.SortedViolations())
	}
}
