package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"medtrace/internal/mederr"
)

func prov(path, hexDigest string) Provenance {
	return Provenance{Path: path, Size: int64(len(hexDigest)), SHA256: hexDigest}
}

func TestIdempotentStageReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run, err := Begin(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	run.SetInputs([]Provenance{prov("sample.webm", "aaa")})
	params := map[string]any{"window_ns": 1000}

	run1, key1, err := run.ShouldRun("media", params)
	if err != nil || !run1 {
		t.Fatalf("first run must execute, got %v err %v", run1, err)
	}
	if err := run.Record("media", params, []OffsetRange{{Input: "sample.webm", Start: 0, End: 42}},
		map[string]any{"blocks": 3}); err != nil {
		t.Fatal(err)
	}

	// Reopen: completed stage must be reused with the same key.
	run2, err := Begin(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	again, key2, err := run2.ShouldRun("media", params)
	if err != nil || again || key1 != key2 {
		t.Fatalf("completed stage must be reused: again=%v key1=%s key2=%s err=%v", again, key1, key2, err)
	}
	if got := run2.CompletedStages(); len(got) != 1 || got[0] != "media" {
		t.Fatalf("completed stages = %v", got)
	}
}

func TestChangedInputInvalidatesOnlyAffectedStages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run, _ := Begin(dir, false)
	run.SetInputs([]Provenance{prov("sample.webm", "aaa")})
	params := map[string]any{}
	if _, _, err := run.ShouldRun("media", params); err != nil {
		t.Fatal(err)
	}
	if err := run.Record("media", params, nil, map[string]any{"v": 1}); err != nil {
		t.Fatal(err)
	}
	// Same stage, different digest: must rerun.
	run.SetInputs([]Provenance{prov("sample.webm", "bbb")})
	rerun, _, err := run.ShouldRun("media", params)
	if err != nil || !rerun {
		t.Fatalf("changed input digest must invalidate the stage, rerun=%v err=%v", rerun, err)
	}
}

func TestChangedParamsInvalidates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run, _ := Begin(dir, false)
	run.SetInputs([]Provenance{prov("e.jsonl", "x")})
	p1 := map[string]any{"window_ns": 1000}
	if err := run.Record("network", p1, nil, nil); err != nil {
		t.Fatal(err)
	}
	p2 := map[string]any{"window_ns": 2000}
	rerun, _, err := run.ShouldRun("network", p2)
	if err != nil || !rerun {
		t.Fatalf("parameter change must invalidate stage: %v %v", rerun, err)
	}
}

func TestCorruptCheckpointRejectedAndRecoverable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run, _ := Begin(dir, false)
	run.SetInputs([]Provenance{prov("a", "h")})
	if err := run.Record("media", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, storeName)
	raw, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the JSON payload (after the opening brace).
	raw[40] ^= 0xFF
	if err := os.WriteFile(cp, raw, filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(dir, false); mederr.AsError(err) == nil ||
		mederr.AsError(err).Category != mederr.CatStateCorrupt {
		t.Fatalf("corrupt checkpoint must fail with state_corrupt, got %v", err)
	}
	// Recovery mode resets and starts clean.
	recovered, err := Begin(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.CompletedStages()) != 0 {
		t.Fatal("recovered run must start with no validated stages")
	}
}

func TestTruncatedCheckpointIsCorrupt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run, _ := Begin(dir, false)
	run.SetInputs([]Provenance{prov("a", "h")})
	if err := run.Record("media", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, storeName)
	raw, _ := os.ReadFile(cp)
	if err := os.WriteFile(cp, raw[:len(raw)/2], filePerm); err != nil {
		t.Fatal(err)
	}
	_, err := Begin(dir, false)
	if mederr.AsError(err) == nil {
		t.Fatal("truncated JSON must be state_corrupt")
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run, _ := Begin(dir, false)
	run.SetInputs([]Provenance{prov("a", "h")})
	if err := run.Record("s1", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), tmpSuffix) {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

func TestSummarizeFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.bin")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SummarizeFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != 3 || len(got.SHA256) != 64 {
		t.Fatalf("bad provenance: %+v", got)
	}
	if _, err := SummarizeFile(filepath.Join(t.TempDir(), "missing")); mederr.AsError(err) == nil {
		t.Fatal("missing file must error")
	}
}

func TestProvenanceAndOffsetsRecorded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run, _ := Begin(dir, false)
	in := Provenance{Path: "sample.webm", Size: 120, SHA256: strings.Repeat("a", 64)}
	run.SetInputs([]Provenance{in})
	offsets := []OffsetRange{{Input: "sample.webm", Start: 10, End: 110}}
	if err := run.Record("media", nil, offsets, map[string]any{"blocks": 9}); err != nil {
		t.Fatal(err)
	}
	reopened, _ := Begin(dir, false)
	st, ok := reopened.Stage("media")
	if !ok || len(st.Offsets) != 1 || st.Offsets[0].Start != 10 || st.Offsets[0].End != 110 {
		t.Fatalf("offset range not preserved: %+v ok=%v", st, ok)
	}
	if h := reopened.InputHashes()["sample.webm"]; h != in.SHA256 {
		t.Fatalf("input hash not preserved: %q", h)
	}
}
