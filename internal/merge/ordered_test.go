package merge

import "testing"

func sample() []Record {
	return []Record{
		{Asset: "b", StartMs: 100, Seq: 2, Stage: "frame", Kind: "keyframe"},
		{Asset: "a", StartMs: 0, Seq: 1, Stage: "subtitle", Kind: "cue"},
		{Asset: "a", StartMs: 0, Seq: 1, Stage: "frame", Kind: "keyframe"},
		{Asset: "a", StartMs: 0, Seq: 1, Stage: "subtitle", Kind: "cue"}, // duplicate
		{Asset: "a", StartMs: 0, Seq: 0, Stage: "frame", Kind: "keyframe"},
	}
}

func TestMergeIsDeterministicAndDedups(t *testing.T) {
	first := Merge(sample())
	second := Merge(sample())
	if first.Duplicates != 1 {
		t.Fatalf("duplicates = %d", first.Duplicates)
	}
	if len(first.Rows) != 4 {
		t.Fatalf("rows = %d", len(first.Rows))
	}
	for i := range first.Rows {
		if first.Rows[i] != second.Rows[i] {
			t.Fatalf("nondeterministic at %d: %v vs %v", i, first.Rows[i], second.Rows[i])
		}
	}
	if v, at := ValidateMonotone(first.Rows); v != 0 {
		t.Fatalf("not monotone at %d", at)
	}
	// Asset order first, then time, then original sequence.
	want := []string{"a/0/frame", "a/0/frame", "a/0/subtitle", "b/100/frame"}
	got := []string{}
	for _, r := range first.Rows {
		got = append(got, r.Asset+"/"+itoa(int(r.StartMs))+"/"+r.Stage)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v", got)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestMergeShuffledInputSameOutput(t *testing.T) {
	in := sample()
	a := Merge(in)
	// Reverse input order; total order must produce identical rows.
	for i, j := 0, len(in)-1; i < j; i, j = i+1, j-1 {
		in[i], in[j] = in[j], in[i]
	}
	b := Merge(in)
	if len(a.Rows) != len(b.Rows) {
		t.Fatalf("row count differs")
	}
	for i := range a.Rows {
		if a.Rows[i] != b.Rows[i] {
			t.Fatalf("row %d differs: %v vs %v", i, a.Rows[i], b.Rows[i])
		}
	}
}

func TestMergeStreams(t *testing.T) {
	s1 := Merge([]Record{{Asset: "a", StartMs: 0, Seq: 1}}).Rows
	s2 := Merge([]Record{{Asset: "a", StartMs: 5, Seq: 2}, {Asset: "a", StartMs: 9, Seq: 3}}).Rows
	out := MergeStreams([][]Record{s1, s2})
	if len(out) != 3 || out[0].StartMs != 0 || out[2].StartMs != 9 {
		t.Fatalf("merged = %+v", out)
	}
	if v, _ := ValidateMonotone(out); v != 0 {
		t.Fatal("merged stream not monotone")
	}
}

func TestMergeEmpty(t *testing.T) {
	r := Merge(nil)
	if len(r.Rows) != 0 || r.Duplicates != 0 {
		t.Fatalf("empty merge = %+v", r)
	}
}
