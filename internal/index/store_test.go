package index

import (
	"path/filepath"
	"testing"
)

func TestPlanRebuildsOnlyChangedDigest(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadOrCreate(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	sourceA := SHA256Hex([]byte("source-a"))
	paramsA, _ := ParamsDigest(map[string]any{"fragment_ms": 2000})
	paramsB, _ := ParamsDigest(map[string]any{"fragment_ms": 1000})
	mk := func(id, source, params, platform string) Spec {
		return Spec{ID: id, Key: Key{source, params, platform}}
	}
	specs := []Spec{
		mk("video-1080p", sourceA, paramsA, "ios"),
		mk("video-720p", sourceA, paramsA, "ios"),
		mk("subtitle", sourceA, paramsA, "ios"),
	}
	first := st.Plan(specs)
	if len(first.Rebuilt) != 3 || len(first.Reused) != 0 {
		t.Fatalf("first=%+v", first)
	}
	for _, s := range specs {
		st.Commit(Record{ID: s.ID, Key: s.Key})
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	// Reload from disk; only one encapsulation parameter changes.
	st2, err := LoadOrCreate(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	specs[0].Key.ParamsSHA256 = paramsB
	second := st2.Plan(specs)
	if len(second.Rebuilt) != 1 || second.Rebuilt[0].ID != "video-1080p" {
		t.Fatalf("rebuilt=%+v", second.Rebuilt)
	}
	if second.Rebuilt[0].Reason != "params_digest_changed" {
		t.Fatalf("reason=%q", second.Rebuilt[0].Reason)
	}
	if len(second.Reused) != 2 {
		t.Fatalf("unrelated assets not reused: %+v", second.Reused)
	}

	// Source digest change affects every dependent asset sharing that source.
	specs[1].Key.SourceSHA256 = SHA256Hex([]byte("source-b"))
	third := st2.Plan(specs)
	reasons := map[string]string{}
	for _, d := range third.Rebuilt {
		reasons[d.ID] = d.Reason
	}
	if reasons["video-720p"] != "source_digest_changed" {
		t.Fatalf("720p reason=%q", reasons["video-720p"])
	}
}

func TestPlanSummaryIsStable(t *testing.T) {
	st := &Store{Assets: map[string]Record{}}
	specs := []Spec{
		{ID: "a", Key: Key{"s", "p", "web"}},
		{ID: "b", Key: Key{"s", "p", "web"}},
	}
	p1 := st.Plan(specs)
	h1 := p1.SummarySHA256()
	p2 := st.Plan(specs)
	h2 := p2.SummarySHA256()
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("summary hashes unstable/invalid: %q %q", h1, h2)
	}
}
