package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/example/mediad/internal/model"
)

func writeProduct(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSaveLoadRoundTrip(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	want := State{
		MaterialID:     "mat-1",
		MaterialDigest: "digest-1",
		Stages: []StageState{
			{Stage: model.StageResample, PacketFrom: 0, PacketTo: 99, OutputDigest: "out-1"},
		},
		ManifestDigest: "man-1",
	}
	if err := st.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("mat-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.MaterialDigest != "digest-1" || len(got.Stages) != 1 {
		t.Fatalf("load mismatch: %+v", got)
	}
}

func TestLoadMissingIsNil(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	got, err := st.Load("never-written")
	if err != nil || got != nil {
		t.Fatalf("missing checkpoint should be (nil,nil), got %v %v", got, err)
	}
}

func TestReusableRejectsStaleDigest(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	if err := st.Save(State{MaterialID: "m", MaterialDigest: "old"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := st.Reusable("m", "new")
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeCheckpointStale {
		t.Fatalf("expected stale error, got %v", err)
	}
}

func TestReusableOnlyIntactProducts(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(filepath.Join(dir, "cp"))
	goodPath := writeProduct(t, dir, "good.bin", "payload")
	goodDigest := Digest([]byte("payload"))
	if err := st.Save(State{
		MaterialID: "m", MaterialDigest: "d",
		Stages: []StageState{
			// Intact product -> reusable.
			{Stage: model.StageResample, ProductPath: goodPath, OutputDigest: goodDigest, PacketTo: 10},
			// Missing product file -> dropped.
			{Stage: model.StageLayout, ProductPath: filepath.Join(dir, "missing.bin"), OutputDigest: "x"},
			// Existing file but wrong digest -> dropped.
			{Stage: model.StageLoudness, ProductPath: goodPath, OutputDigest: "wrong"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	keep, ok, err := st.Reusable("m", "d")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(keep) != 1 || keep[0].Stage != model.StageResample {
		t.Fatalf("reusable = %+v ok=%v", keep, ok)
	}
}

func TestAtomicSaveNoTempLeak(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(dir)
	if err := st.Save(State{MaterialID: "m", MaterialDigest: "d"}); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "cp-*")); len(matches) != 0 {
		t.Fatalf("temp files leaked: %v", matches)
	}
}
