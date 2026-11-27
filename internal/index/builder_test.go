package index_test

import (
	"errors"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/index"
)

func sampleIndex() *index.Index {
	ix := index.New()
	// Track 1: sync at t=0 and t=4, non-sync rows in between, 25 Hz clock.
	entries := []index.Entry{
		{Track: 1, Sample: 0, Time: clock.MustMake(0, 25), FileOffset: 100, Size: 10, Sync: true},
		{Track: 1, Sample: 1, Time: clock.MustMake(1, 25), FileOffset: 110, Size: 10, Sync: false},
		{Track: 1, Sample: 2, Time: clock.MustMake(2, 25), FileOffset: 120, Size: 10, Sync: false},
		{Track: 1, Sample: 3, Time: clock.MustMake(3, 25), FileOffset: 130, Size: 10, Sync: false},
		{Track: 1, Sample: 4, Time: clock.MustMake(4, 25), FileOffset: 140, Size: 10, Sync: true},
		{Track: 1, Sample: 5, Time: clock.MustMake(5, 25), FileOffset: 150, Size: 10, Sync: false},
	}
	// Add out of order to prove Add re-sorts.
	ix.Add(entries[3:])
	ix.Add(entries[:3])
	return ix
}

func TestLookupReturnsContainingSync(t *testing.T) {
	ix := sampleIndex()
	// A target between sync points resolves to the preceding sync.
	got, err := ix.Lookup(1, clock.MustMake(3, 25))
	if err != nil {
		t.Fatal(err)
	}
	if got.Sample != 0 || !got.Sync || got.FileOffset != 100 {
		t.Fatalf("lookup mid-range = %+v", got)
	}
	got2, err := ix.Lookup(1, clock.MustMake(5, 25))
	if err != nil {
		t.Fatal(err)
	}
	if got2.Sample != 4 {
		t.Fatalf("lookup after second sync = sample %d", got2.Sample)
	}
	// Exact sync time returns that sync.
	got3, err := ix.Lookup(1, clock.MustMake(4, 25))
	if err != nil || got3.Sample != 4 {
		t.Fatalf("exact sync lookup = %+v err=%v", got3, err)
	}
}

func TestLookupGapAndCoverage(t *testing.T) {
	ix := sampleIndex()
	// Target beyond coverage: explicit gap error.
	_, err := ix.Lookup(1, clock.MustMake(99, 25))
	if !errors.Is(err, clock.ErrGap) {
		t.Fatalf("out-of-range: want ErrGap, got %v", err)
	}
	// Unknown track: gap error too.
	if _, err := ix.Lookup(42, clock.MustMake(0, 25)); !errors.Is(err, clock.ErrGap) {
		t.Fatalf("unknown track: %v", err)
	}
	// A track whose first entry is non-sync has no seek point before an early
	// target: must report a gap rather than return a non-sync row.
	noSync := index.New()
	noSync.Add([]index.Entry{
		{Track: 9, Time: clock.MustMake(1, 25), Sync: false},
		{Track: 9, Time: clock.MustMake(2, 25), Sync: false},
	})
	if _, err := noSync.Lookup(9, clock.MustMake(2, 25)); !errors.Is(err, clock.ErrGap) {
		t.Fatalf("no sync sample: %v", err)
	}
}

func TestIndexCountsAndOrdering(t *testing.T) {
	ix := sampleIndex()
	if ix.EntryCount() != 6 || ix.SyncCount() != 2 {
		t.Fatalf("counts entries=%d sync=%d", ix.EntryCount(), ix.SyncCount())
	}
	list := ix.Entries(1)
	for i := 1; i < len(list); i++ {
		if list[i].Time.Cmp(list[i-1].Time) < 0 {
			t.Fatal("entries not sorted by time")
		}
	}
}
