package fixture

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestParseRates(t *testing.T) {
	rates, err := ParseRates("3200000, 800000,1600000")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{800000, 1600000, 3200000}
	for i, rate := range want {
		if rates[i] != rate {
			t.Fatalf("rates = %v, want %v", rates, want)
		}
	}
	for _, bad := range []string{"", "0", "-5", "1,1", "1,,2", "abc"} {
		if _, err := ParseRates(bad); err == nil {
			t.Errorf("ParseRates(%q) succeeded, want error", bad)
		}
	}
}

func TestParseDrops(t *testing.T) {
	drops, err := ParseDrops([]string{"1600000:5", "800000:0"})
	if err != nil {
		t.Fatal(err)
	}
	if drops[1600000] != 5 || drops[800000] != 0 {
		t.Fatalf("drops = %v", drops)
	}
	for _, bad := range []string{"1600000", "1600000:", ":5", "x:1", "-1:1", "1600000:5x"} {
		if _, err := ParseDrops([]string{bad}); err == nil {
			t.Errorf("ParseDrops(%q) succeeded, want error", bad)
		}
	}
	if _, err := ParseDrops([]string{"1600000:5", "1600000:6"}); err == nil {
		t.Error("repeated drop rate accepted, want error")
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	plan := Plan{Root: rootA, Rates: []int{800000, 1600000}, Segments: 12}
	if err := Generate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	plan.Root = rootB
	if err := Generate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	namesA, _ := os.ReadDir(rootA)
	namesB, _ := os.ReadDir(rootB)
	var a, b []string
	for _, entry := range namesA {
		a = append(a, entry.Name())
	}
	for _, entry := range namesB {
		b = append(b, entry.Name())
	}
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		t.Fatalf("file count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("names differ at %d: %s vs %s", i, a[i], b[i])
		}
	}
	for _, name := range a {
		bodyA, err := os.ReadFile(filepath.Join(rootA, name))
		if err != nil {
			t.Fatal(err)
		}
		bodyB, err := os.ReadFile(filepath.Join(rootB, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(bodyA) != string(bodyB) {
			t.Fatalf("%s differs between runs", name)
		}
	}
}

func TestGenerateRejectsBadPlan(t *testing.T) {
	cases := []Plan{
		{Root: t.TempDir(), Rates: []int{800000}, Segments: 0},
		{Root: t.TempDir(), Rates: nil, Segments: 4},
	}
	for _, plan := range cases {
		if err := Generate(context.Background(), plan); err == nil {
			t.Errorf("Generate(%+v) succeeded, want error", plan)
		}
	}
}

func TestGenerateHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	err := Generate(ctx, Plan{Root: root, Rates: []int{800000}, Segments: 4})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("cancelled run left files behind: %v", entries)
	}
}

func TestDurationsSumToTwelveSeconds(t *testing.T) {
	var total int64
	for _, duration := range RateDurations {
		total += duration
	}
	if total != 12000 {
		t.Fatalf("RateDurations sums to %d, want 12000", total)
	}
	if len(RateDurations) != 12 {
		t.Fatalf("RateDurations has %d entries, want 12", len(RateDurations))
	}
}
