package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bytes"
	"mediacore/index"
	"mediacore/internal/fixture"
	"mediacore/manifest"
	"mediacore/media"
	"mediacore/pipeline"
)

// writeDemoArtifacts generates a clean stream and writes frames, index and
// manifest into dir using the same production code paths.
func writeDemoArtifacts(t *testing.T, dir string) {
	t.Helper()
	plan := fixture.DefaultPlan()
	frames, err := plan.BuildFrames(fixture.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	data, err := fixture.EncodeFrames(frames)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	res, err := pipeline.Run(idx, pipeline.NewBufferSource(data, media.DefaultLimits()),
		pipeline.DefaultConfig(media.DefaultTimescale))
	if err != nil {
		t.Fatal(err)
	}
	timing, err := manifest.BindTiming(idx.Entries, res.MediaDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Build(idx, res, manifest.Options{TargetKBPS: 900, Timing: timing})
	if err != nil {
		t.Fatal(err)
	}
	ib, _ := idx.Marshal()
	mb, _ := m.Marshal()
	os.WriteFile(filepath.Join(dir, "frames.bin"), data, 0o644)
	os.WriteFile(filepath.Join(dir, "index.json"), ib, 0o644)
	os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644)
}

func TestVerifyCleanArtifacts(t *testing.T) {
	dir := t.TempDir()
	writeDemoArtifacts(t, dir)
	rep, code, err := Run(Input{
		FramesPath:   filepath.Join(dir, "frames.bin"),
		IndexPath:    filepath.Join(dir, "index.json"),
		ManifestPath: filepath.Join(dir, "manifest.json"),
	}, manifest.DefaultVerifyOptions())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v failures=%+v", code, err, rep.Failures)
	}
	if !rep.IndexMatchesInput || !rep.IndexComplete || !rep.ManifestOK {
		t.Fatalf("report=%+v", rep)
	}
	if rep.Detail.AlignmentMS > 2 {
		t.Fatalf("alignment=%d", rep.Detail.AlignmentMS)
	}
}

func TestVerifyRejectsChangedInput(t *testing.T) {
	dir := t.TempDir()
	writeDemoArtifacts(t, dir)
	// Append a byte so the input digest no longer matches the index.
	f, _ := os.OpenFile(filepath.Join(dir, "frames.bin"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte{0})
	f.Close()
	_, code, err := Run(Input{
		FramesPath:   filepath.Join(dir, "frames.bin"),
		IndexPath:    filepath.Join(dir, "index.json"),
		ManifestPath: filepath.Join(dir, "manifest.json"),
	}, manifest.DefaultVerifyOptions())
	if err == nil || code != 1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if media.CategoryOf(err) != media.CatIndexInput {
		t.Fatalf("category=%s", media.CategoryOf(err))
	}
}

func TestVerifyRejectsTamperedManifest(t *testing.T) {
	dir := t.TempDir()
	writeDemoArtifacts(t, dir)
	raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	stats := doc["stats"].(map[string]any)
	stats["frame_count"] = float64(7)
	patched, _ := json.Marshal(doc)
	os.WriteFile(filepath.Join(dir, "manifest.json"), patched, 0o644)
	_, code, err := Run(Input{
		FramesPath:   filepath.Join(dir, "frames.bin"),
		IndexPath:    filepath.Join(dir, "index.json"),
		ManifestPath: filepath.Join(dir, "manifest.json"),
	}, manifest.DefaultVerifyOptions())
	if err == nil || code != 1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestVerifyMissingFileIsParameterError(t *testing.T) {
	_, code, err := Run(Input{FramesPath: "nope", IndexPath: "nope", ManifestPath: "nope"},
		manifest.DefaultVerifyOptions())
	if err == nil || code != 2 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
