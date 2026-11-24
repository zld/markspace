package clock

import "testing"

func TestCmpUsesExactRationals(t *testing.T) {
	a := New(1, 3)
	b := New(2, 6)
	if got := a.Cmp(b); got != 0 {
		t.Fatalf("1/3 vs 2/6: got %d want 0", got)
	}
	if got := New(1001, 48000).Cmp(New(1000, 48000)); got <= 0 {
		t.Fatalf("expected later time to sort after, got %d", got)
	}
	if got := New(90000, 90000).Cmp(New(48001, 48000)); got >= 0 {
		t.Fatalf("expected 1s < 1.00002s, got %d", got)
	}
}

func TestRescaleRoundAndExact(t *testing.T) {
	got, exact, err := New(1, 3).Rescale(1000)
	if err != nil || exact || got != 333 {
		t.Fatalf("1/3s: got %d exact=%v err=%v", got, exact, err)
	}
	got, exact, err = New(2, 3).Rescale(1000)
	if err != nil || exact || got != 667 {
		t.Fatalf("2/3s: got %d exact=%v err=%v", got, exact, err)
	}
	got, exact, err = New(-2, 3).Rescale(1000)
	if err != nil || exact || got != -667 {
		t.Fatalf("-2/3s: got %d exact=%v err=%v", got, exact, err)
	}
	got, exact, err = New(90000, 90000).Rescale(1000)
	if err != nil || !exact || got != 1000 {
		t.Fatalf("1s: got %d exact=%v err=%v", got, exact, err)
	}
	got, exact, err = New(1000, 1000).Rescale(90000)
	if err != nil || !exact || got != 90000 {
		t.Fatalf("1s->90k: got %d exact=%v err=%v", got, exact, err)
	}
}

func TestUnify(t *testing.T) {
	got, err := Unify(90000, 48000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 720000 {
		t.Fatalf("unify: got %d want 720000", got)
	}
	if _, ok, _ := New(1, 90000).Rescale(got); !ok {
		t.Fatal("rescale to unified timescale must be exact")
	}
	if _, err := Unify(0, 0); err == nil {
		t.Fatal("expected error for zero timescales")
	}
}

func TestStableSortKey(t *testing.T) {
	values := []Time{New(2, 48000), New(1, 90000), New(1, 48000), New(1, 1000)}
	first := make([]Time, len(values))
	copy(first, values)
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if first[j].Cmp(first[i]) < 0 {
				first[i], first[j] = first[j], first[i]
			}
		}
	}
	if first[0].Cmp(New(1, 90000)) != 0 || first[3].Cmp(New(1, 1000)) != 0 {
		t.Fatalf("unexpected order: %v", first)
	}
}
