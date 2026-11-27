package report

import (
	"encoding/json"
	"testing"
)

func TestManifestDigestDeterministic(t *testing.T) {
	build := func() *Summary {
		return &Summary{
			Seed: 42,
			Items: []Item{
				{MaterialID: "a", TotalPackets: 10, OutputBytes: 100, Status: StatusSatisfied},
				{MaterialID: "b", TotalPackets: 5, Status: StatusIsolated, Isolated: true},
			},
			Budget: Budget{BudgetMs: 5000, ElapsedMs: 120, MaxBytes: 65536},
		}
	}
	d1, err := build().ComputeManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := build().ComputeManifestDigest()
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 || len(d1) != 64 {
		t.Fatalf("digest not deterministic: %q vs %q", d1, d2)
	}
}

func TestDigestChangesWithContent(t *testing.T) {
	base := &Summary{Seed: 1, Items: []Item{{MaterialID: "a", Status: StatusSatisfied}}}
	d1, _ := base.ComputeManifestDigest()
	changed := &Summary{Seed: 1, Items: []Item{{MaterialID: "a", Status: StatusUnmet}}}
	d2, _ := changed.ComputeManifestDigest()
	if d1 == d2 {
		t.Fatal("digest must change when an item status changes")
	}
}

func TestJSONIncludesDigestAndIsValid(t *testing.T) {
	s := &Summary{Seed: 7, Items: []Item{{MaterialID: "x"}}, Budget: Budget{BudgetMs: 1}}
	raw, err := s.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if decoded["manifestSha256"] == nil || decoded["manifestSha256"] == "" {
		t.Fatal("manifest digest missing from JSON")
	}
}
