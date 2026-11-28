package report

import (
	"encoding/json"
	"strings"
	"testing"

	"mediasync/internal/fault"
)

func TestDeterministicHashAndOrder(t *testing.T) {
	metrics := Metrics{SyncErrorMS: 1.5, LossRecovered: 1, LossTotal: 1, LatencyP95MS: 12}
	// Faults deliberately inserted out of order.
	faults := []*fault.Fault{
		fault.New(fault.ErrOutOfBounds, "subtitle", 300, "later offset"),
		fault.New(fault.ErrTruncated, "audio", 100, "earlier offset"),
		fault.New(fault.ErrTruncated, "audio", 100, "same offset, detail sorts"),
	}

	r1, err := Build(metrics, faults)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Build(metrics, faults)
	if err != nil {
		t.Fatal(err)
	}
	if r1.ContentHash != r2.ContentHash {
		t.Fatal("identical inputs must hash identically")
	}
	if len(r1.Faults) != 3 {
		t.Fatalf("want 3 faults, got %d", len(r1.Faults))
	}
	// Sorted by offset then detail.
	if r1.Faults[0].Offset != 100 || r1.Faults[2].Offset != 300 {
		t.Fatalf("faults not offset-sorted: %+v", r1.Faults)
	}
	if r1.Faults[0].Detail >= r1.Faults[1].Detail {
		t.Fatalf("tie-break by detail wrong: %q vs %q", r1.Faults[0].Detail, r1.Faults[1].Detail)
	}

	// Rendered JSON contains every contracted field.
	out, err := r1.RenderJSON()
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)
	for _, key := range []string{
		"sync_error_ms", "loss_recovered", "loss_total", "latency_p95_ms",
		"error_code", "offset", "content_hash",
	} {
		if !strings.Contains(doc, key) {
			t.Fatalf("rendered output missing contracted field %q", key)
		}
	}
}

func TestHashChangesWhenContentChanges(t *testing.T) {
	base := Metrics{SyncErrorMS: 1}
	r1, _ := Build(base, nil)
	base.SyncErrorMS = 2
	r2, _ := Build(base, nil)
	if r1.ContentHash == r2.ContentHash {
		t.Fatal("different metrics must produce different hashes")
	}
}

func TestFaultViewCarriesCodeAndName(t *testing.T) {
	r, err := Build(Metrics{}, []*fault.Fault{
		fault.New(fault.ErrWrapAmbiguous, "stream", 4242, "half wrap"),
	})
	if err != nil {
		t.Fatal(err)
	}
	fv := r.Faults[0]
	if fv.Code != int(fault.ErrWrapAmbiguous) || fv.Name != "wrap_ambiguous" || fv.Offset != 4242 {
		t.Fatalf("fault view wrong: %+v", fv)
	}
	var raw map[string]any
	if err := json.Unmarshal(mustJSON(t, r), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["content_hash"]; !ok {
		t.Fatal("content_hash missing from JSON")
	}
}

func TestRecoveryRate(t *testing.T) {
	cases := []struct {
		rec, total int
		want       float64
	}{
		{0, 0, 100},
		{1, 1, 100},
		{1, 2, 50},
		{0, 4, 0},
	}
	for _, c := range cases {
		m := Metrics{LossRecovered: c.rec, LossTotal: c.total}
		if got := m.RecoveryRate(); got != c.want {
			t.Errorf("rec=%d total=%d: got %f want %f", c.rec, c.total, got, c.want)
		}
	}
}

func TestHasValidationFaults(t *testing.T) {
	cancelOnly, _ := Build(Metrics{}, []*fault.Fault{
		fault.New(fault.ErrCanceled, "", -1, "bye"),
	})
	if cancelOnly.HasValidationFaults() {
		t.Fatal("cancel-only report must not count as validation failure")
	}
	withValidation, _ := Build(Metrics{}, []*fault.Fault{
		fault.New(fault.ErrTruncated, "", 9, "cut"),
	})
	if !withValidation.HasValidationFaults() {
		t.Fatal("a truncated fault must count as validation failure")
	}
}

func mustJSON(t *testing.T, r *Report) []byte {
	t.Helper()
	b, err := r.RenderJSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
