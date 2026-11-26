package merge

import "testing"

func samples() []Sample {
	mk := func(v string, ticks, off, length int64, hash string) Sample {
		return Sample{Version: v, Track: 1, Ticks: ticks, Scale: 90000, SrcOffset: off, Length: length, Hash: hash}
	}
	return []Sample{
		mk("va", 0, 0, 30, "a0"), mk("vb", 0, 0, 30, "a0"),
		mk("va", 3000, 30, 30, "a1"), mk("vb", 3000, 30, 31, "DIFF"),
		mk("va", 6000, 60, 30, "a2"), // vb missing here
	}
}

func TestMap(t *testing.T) {
	res := Map(samples(), []string{"va", "vb"})
	if res.Mapped != 3 || res.Missing != 1 || res.Conflicts != 1 {
		t.Fatalf("got %s, want mapped=3 missing=1 conflicts=1", res)
	}
	// item 0: identical, both committed offsets
	it := res.Items[0]
	if it.Conflict || it.Chosen != "va" {
		t.Fatalf("item0: %+v", it)
	}
	if it.Entries[0].OutOff != 0 || it.Entries[1].OutOff != -1 {
		t.Fatalf("item0 offsets: %+v", it.Entries)
	}
	// item 1: conflict, byte diff recorded
	it = res.Items[1]
	if !it.Conflict || it.Entries[1].ByteDiff != 1 || it.Entries[1].Dt != 0 {
		t.Fatalf("item1: %+v", it)
	}
	// item 2: vb missing
	it = res.Items[2]
	if len(it.Missing) != 1 || it.Missing[0] != "vb" {
		t.Fatalf("item2 missing: %+v", it.Missing)
	}
	if it.Entries[0].OutOff != 60 {
		t.Fatalf("item2 out offset = %d, want 60", it.Entries[0].OutOff)
	}
}

func TestMapNormalizesTimescales(t *testing.T) {
	s := []Sample{
		{Version: "va", Track: 1, Ticks: 3000, Scale: 90000, Hash: "x", Length: 10},
		{Version: "vb", Track: 1, Ticks: 100, Scale: 3000, Hash: "x", Length: 10},
	}
	res := Map(s, []string{"va", "vb"})
	if res.Mapped != 1 || res.Conflicts != 0 {
		t.Fatalf("got %s, want one shared position", res)
	}
	if res.Items[0].Ticks != 1 || res.Items[0].Scale != 30 {
		t.Fatalf("position not normalized: %+v", res.Items[0])
	}
}
