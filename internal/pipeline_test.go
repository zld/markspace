package kernel

import "testing"

func TestCleanPipelineAcceptanceMetricsAndCache(t *testing.T) {
	dir := t.TempDir()
	first, err := Run(RunOptions{Fixture: "clean", Out: dir})
	if err != nil {
		t.Fatal(err)
	}
	if first.Wraps != 1 || first.CutCount != 2 || first.SilenceMS != 1000 {
		t.Fatalf("clean metrics wraps=%d cuts=%d silence=%d", first.Wraps, first.CutCount, first.SilenceMS)
	}
	if len(first.Stages) != 4 || first.Stages[0] != StageIngest || first.Stages[3] != StageManifest {
		t.Fatalf("stages=%v", first.Stages)
	}
	if first.Cache.Created != 2 || first.Cache.Reused != 0 || !first.Validation.Valid {
		t.Fatalf("first cache=%#v validation=%#v", first.Cache, first.Validation)
	}

	second, err := Run(RunOptions{Fixture: "clean", Out: dir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Cache.Created != 0 || second.Cache.Reused != 2 {
		t.Fatalf("second cache=%#v", second.Cache)
	}

	mutated, err := Run(RunOptions{Fixture: "clean", Out: dir, MutateSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if mutated.SourceDigest == first.SourceDigest || mutated.Cache.Invalidated != 2 {
		t.Fatalf("mutation digest changed=%v cache=%#v", mutated.SourceDigest != first.SourceDigest, mutated.Cache)
	}
}
