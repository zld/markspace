package scan_test

import (
	"errors"
	"testing"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/scan"
)

func faultAs(err error, target **fault.Error) bool { return errors.As(err, target) }

func faultStrings(list []*fault.Error) []string { return fault.Strings(list) }

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// assertLocated requires every fault to carry a non-negative byte offset.
func assertLocated(t *testing.T, faults []*fault.Error) {
	t.Helper()
	if len(faults) == 0 {
		t.Fatalf("expected at least one fault")
	}
	for _, f := range faults {
		if f.Offset < 0 {
			t.Fatalf("fault has no byte offset: %v", f)
		}
	}
}

func assertMonotonic(t *testing.T, res *scan.Result) {
	t.Helper()
	for _, tr := range res.Index.Tracks {
		var prev clock.Nanos
		for i, s := range tr.Samples {
			if i > 0 && s.DTS < prev {
				t.Fatalf("track %d sample %d goes backwards", tr.ID, i)
			}
			prev = s.DTS
		}
	}
}
