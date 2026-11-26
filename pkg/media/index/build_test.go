package index

import (
	"os"
	"path/filepath"
	"testing"

	"cmafrepair/pkg/media/cmaf"
)

const syncFlags uint32 = 0x02000000 // sample_depends_on = 2 (sync sample)

// buildSyncFragment writes a fragment whose every other sample is a sync
// point. trun flags 0x700 = duration + size + per-sample flags.
func buildSyncFragment(seq, tfdt uint64, nSamples int) []byte {
	trun := cmaf.NewBox(cmaf.BoxTrun).FullHeader(1, 0x000700).U32(uint32(nSamples))
	for i := 0; i < nSamples; i++ {
		var flags uint32
		if i%2 == 0 {
			flags = syncFlags
		}
		trun.U32(9000).U32(100).U32(flags) // 100 ms per sample at 90k
	}
	tfhd := cmaf.NewBox(cmaf.BoxTfhd).FullHeader(0, 0).U32(1)
	tfdtBox := cmaf.NewBox(cmaf.BoxTfdt).FullHeader(1, 0).U64(tfdt)
	mfhd := cmaf.NewBox(cmaf.BoxMfhd).FullHeader(0, 0).U32(uint32(seq))
	traf := cmaf.NewBox(cmaf.BoxTraf).Child(tfhd).Child(tfdtBox).Child(trun)
	moof := cmaf.NewBox(cmaf.BoxMoof).Child(mfhd).Child(traf)
	mdat := cmaf.NewBox(cmaf.BoxMdat).Bytes(make([]byte, 100*nSamples))
	return append(moof.Build(), mdat.Build()...)
}

func parseTemp(t *testing.T, data []byte) *cmaf.Fragment {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.m4s")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := cmaf.ParseFragment(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestIndexBuildsOneEntryPerSyncSample(t *testing.T) {
	b := NewBuilder("video_90000", 90000)
	// Two fragments, 4 samples each -> 2 sync samples per fragment.
	f1 := parseTemp(t, buildSyncFragment(1, 0, 4))
	e1 := cmaf.TimelineEntry{Name: f1.Name, Sequence: 1, FixedTfdt: 0, Status: cmaf.StatusIntact}
	if err := b.AddFragment(f1, e1); err != nil {
		t.Fatal(err)
	}
	f2 := parseTemp(t, buildSyncFragment(2, 36000, 4))
	e2 := cmaf.TimelineEntry{Name: f2.Name, Sequence: 2, FixedTfdt: 36000, Status: cmaf.StatusIntact}
	if err := b.AddFragment(f2, e2); err != nil {
		t.Fatal(err)
	}
	ix := b.Build()
	if len(ix.Entries) != 4 {
		t.Fatalf("want 4 sync entries, got %d: %+v", len(ix.Entries), ix.Entries)
	}
	// Entries must be sorted on shared time: 0, 200, 400, 600 ms.
	want := []int64{0, 200, 400, 600}
	for i, e := range ix.Entries {
		if e.SharedMs != want[i] {
			t.Fatalf("entry %d time=%d want %d", i, e.SharedMs, want[i])
		}
	}
}

func TestLookupReturnsNearestDecodableBeforeTarget(t *testing.T) {
	b := NewBuilder("v", 90000)
	f := parseTemp(t, buildSyncFragment(1, 0, 6)) // sync at 0,200,400 ms
	if err := b.AddFragment(f, cmaf.TimelineEntry{Status: cmaf.StatusIntact}); err != nil {
		t.Fatal(err)
	}
	ix := b.Build()

	// Target 350 ms -> nearest sync at or before is 200 ms.
	e, ok := ix.Lookup(350)
	if !ok || e.SharedMs != 200 {
		t.Fatalf("lookup(350)=%+v ok=%v want 200ms", e, ok)
	}
	// Exact hit.
	if e, _ := ix.Lookup(400); e.SharedMs != 400 {
		t.Fatalf("exact lookup failed: %d", e.SharedMs)
	}
	// Before the first point -> no decodable predecessor.
	if _, ok := ix.Lookup(-1); ok {
		t.Fatal("lookup before first entry must report none")
	}
	// LookupOrFirst falls back to the earliest point.
	first, ok := ix.LookupOrFirst(-1)
	if !ok || first.SharedMs != 0 {
		t.Fatalf("LookupOrFirst fallback wrong: %+v ok=%v", first, ok)
	}
}

func TestLookupSkipsGapEntries(t *testing.T) {
	b := NewBuilder("v", 90000)
	// Gap at 100 ms (no real sync there), real sync points at 0,200,400 ms.
	b.AddGap(cmaf.TimelineEntry{Name: "missing.m4s", Sequence: 2, StartMs: 100, Status: cmaf.StatusGap})
	f := parseTemp(t, buildSyncFragment(1, 0, 6))
	if err := b.AddFragment(f, cmaf.TimelineEntry{Status: cmaf.StatusIntact}); err != nil {
		t.Fatal(err)
	}
	ix := b.Build()
	if ix.GapCount() != 1 || ix.DecodableCount() != 3 {
		t.Fatalf("counts wrong: gap=%d decodable=%d", ix.GapCount(), ix.DecodableCount())
	}
	// Target 150 ms: the gap at 100 is not decodable, so fall to the 0 ms sync.
	e, ok := ix.Lookup(150)
	if !ok || e.Gap || e.SharedMs != 0 {
		t.Fatalf("lookup must skip GAP: %+v ok=%v", e, ok)
	}
}

func TestSampleOffsetsAdvanceBySize(t *testing.T) {
	b := NewBuilder("v", 90000)
	f := parseTemp(t, buildSyncFragment(1, 0, 4))
	if err := b.AddFragment(f, cmaf.TimelineEntry{Status: cmaf.StatusIntact}); err != nil {
		t.Fatal(err)
	}
	ix := b.Build()
	// Sync samples are index 0 (offset 0) and index 2 (offset 200 bytes).
	if ix.Entries[0].SampleOffset != 0 || ix.Entries[1].SampleOffset != 200 {
		t.Fatalf("sample offsets wrong: %d, %d",
			ix.Entries[0].SampleOffset, ix.Entries[1].SampleOffset)
	}
	if ix.Entries[0].FileOffset != f.Mdat.PayloadOff {
		t.Fatal("file offset must point at mdat payload")
	}
}
