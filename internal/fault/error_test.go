package fault_test

import (
	"strings"
	"testing"

	"mediacore/internal/fault"
)

func TestErrorRendersCorrelationFields(t *testing.T) {
	e := fault.New(fault.CodeTruncatedBox, "s.fmp4", 4096, "moof", "declares 104 bytes").WithFragment(12)
	got := e.Error()
	for _, want := range []string{"truncated_box", "file=s.fmp4", "fragment=12", "box=moof", "offset=4096", "declares 104 bytes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("message %q missing %q", got, want)
		}
	}
}

func TestCollectorOrderAndFirstOffset(t *testing.T) {
	var c fault.Collector
	c.Addf(fault.CodeIndexGap, "s.fmp4", 900, "sidx", "short index")
	c.Addf(fault.CodeTfdtRegression, "s.fmp4", 100, "tfdt", "regressed")
	c.Addf(fault.CodeTfdtRegression, "s.fmp4", 200, "tfdt", "regressed again")
	if c.Len() != 3 {
		t.Fatalf("len=%d", c.Len())
	}
	// Collection order is preserved for reporting.
	if c.Errors()[0].Code != fault.CodeIndexGap {
		t.Fatal("collector reordered entries")
	}
	first, ok := c.FirstByOffset()
	if !ok || first.Offset != 100 {
		t.Fatalf("first offset=%v", first)
	}
	if n, ok := c.Count(fault.CodeTfdtRegression); !ok || n != 2 {
		t.Fatalf("count=%d ok=%v", n, ok)
	}
	codes := c.Codes()
	if len(codes) != 2 || codes[0] != fault.CodeIndexGap || codes[1] != fault.CodeTfdtRegression {
		t.Fatalf("codes=%v", codes)
	}
}

func TestSortByOffsetGroupsByFile(t *testing.T) {
	errs := []*fault.Error{
		fault.New(fault.CodeBadPayload, "b.fmp4", 10, "moof", ""),
		fault.New(fault.CodeBadPayload, "a.fmp4", 50, "moof", ""),
		fault.New(fault.CodeBadPayload, "a.fmp4", 5, "moof", ""),
	}
	fault.SortByOffset(errs)
	if errs[0].File != "a.fmp4" || errs[0].Offset != 5 || errs[1].Offset != 50 || errs[2].File != "b.fmp4" {
		t.Fatalf("sorted=%v", []string{errs[0].Error(), errs[1].Error(), errs[2].Error()})
	}
}
