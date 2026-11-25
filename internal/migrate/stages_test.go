package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"example.com/assetcheck/internal/manifest"
	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

const manifestV1 = `{
  "version": 1,
  "asset_id": "game-ep01",
  "languages": ["jpn", "eng"],
  "max_channels": 8,
  "sequence_base": 0,
  "audio": {
    "2": {"rate_hz": {"min": 48000, "max": 48000}, "layout": "stereo"}
  },
  "video": {
    "1": {"colour": {"matrix": 9, "transfer": 16, "primaries": 9, "range": 1}}
  },
  "tracks": [
    {"number": 1, "type": "video", "codec_id": "V_MPEG4/ISO/AVC", "language": "jpn"},
    {"number": 2, "type": "audio", "codec_id": "A_OPUS", "language": "jpn"}
  ]
}`

func testManifest(t *testing.T, doc string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func testAsset() *media.Asset {
	a := &media.Asset{
		Path: "fixture.mkv", Size: 4096, SHA256: "abc123", DocType: "matroska",
		Version: 4, SequenceBase: -1,
	}
	a.Tracks = tracksFixture
	a.Fragments = []media.Fragment{
		{TrackNumber: 1, Order: 0, Start: 0, Offset: 100},
		{TrackNumber: 2, Order: 1, Start: 20_000_000, Offset: 140},
	}
	return a
}

func TestRunIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	a := testAsset()
	r1, err := Run(Options{Out: dir, Manifest: m, Asset: a})
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if r1.FinalDigest == "" {
		t.Fatal("no final digest")
	}
	if len(r1.Records) != 3 {
		t.Fatalf("records = %d", len(r1.Records))
	}
	for i, stage := range Stages {
		if r1.Records[i].Stage != stage {
			t.Fatalf("stage %d = %s", i, r1.Records[i].Stage)
		}
		if r1.Records[i].OutputDigest == "" || r1.Records[i].InputDigest == "" {
			t.Fatalf("stage %s missing digests: %+v", stage, r1.Records[i])
		}
	}

	dir2 := t.TempDir()
	r2, err := Run(Options{Out: dir2, Manifest: m, Asset: a})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if r1.FinalDigest != r2.FinalDigest {
		t.Fatalf("digests differ: %s vs %s", r1.FinalDigest, r2.FinalDigest)
	}
	if r1.Generation != r2.Generation {
		t.Fatalf("generations differ: %s vs %s", r1.Generation, r2.Generation)
	}
	// Stage output digests must also repeat.
	for i := range r1.Records {
		if r1.Records[i].OutputDigest != r2.Records[i].OutputDigest {
			t.Fatalf("stage %s output digest differs", r1.Records[i].Stage)
		}
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStopAfterLeavesNoCommittedOutput(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	res, err := Run(Options{Out: dir, Manifest: m, Asset: testAsset(), StopAfter: StageNormalize})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalDigest != "" {
		t.Fatalf("early stop committed: %s", res.FinalDigest)
	}
	if len(res.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(res.Records))
	}
	if _, err := os.Stat(filepath.Join(dir, "CURRENT")); !os.IsNotExist(err) {
		t.Fatalf("CURRENT exists after early stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, res.Generation, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("generation written after early stop: %v", err)
	}
}

func TestResumeAfterStopMatchesUninterruptedHash(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	a := testAsset()
	full, err := Run(Options{Out: t.TempDir(), Manifest: m, Asset: a})
	if err != nil {
		t.Fatalf("full: %v", err)
	}

	if _, err := Run(Options{Out: dir, Manifest: m, Asset: a, StopAfter: StageNormalize}); err != nil {
		t.Fatalf("partial: %v", err)
	}
	res, err := Run(Options{Out: dir, Manifest: m, Asset: a, Resume: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.Resumed {
		t.Fatal("result not marked resumed")
	}
	if res.FinalDigest != full.FinalDigest {
		t.Fatalf("resumed digest %s != uninterrupted %s", res.FinalDigest, full.FinalDigest)
	}
	if res.Generation != full.Generation {
		t.Fatalf("resumed generation %s != %s", res.Generation, full.Generation)
	}
	// The resumed run must reuse the parse and normalize checkpoints.
	skipped := 0
	for _, r := range res.Records {
		if r.Skipped {
			skipped++
		}
	}
	if skipped < 2 {
		t.Fatalf("resume reran completed stages: %+v", res.Records)
	}
}

func TestResumeAfterWriteCommit(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	a := testAsset()
	if _, err := Run(Options{Out: dir, Manifest: m, Asset: a, StopAfter: StageWrite}); err != nil {
		t.Fatalf("partial: %v", err)
	}
	res, err := Run(Options{Out: dir, Manifest: m, Asset: a, Resume: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.FinalDigest == "" {
		t.Fatal("resume after write did not commit")
	}
	cur, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, c, err := ReadCommitted(cur)
	if err != nil {
		t.Fatalf("read committed: %v", err)
	}
	if c.Digest != res.FinalDigest {
		t.Fatalf("CURRENT digest %s != %s", c.Digest, res.FinalDigest)
	}
	if len(data) == 0 {
		t.Fatal("committed manifest empty")
	}
}

func TestResumeMismatchedInputRollsBack(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	a := testAsset()
	if _, err := Run(Options{Out: dir, Manifest: m, Asset: a, StopAfter: StageNormalize}); err != nil {
		t.Fatalf("partial: %v", err)
	}
	other := testAsset()
	other.SHA256 = "different-input-hash"
	res, err := Run(Options{Out: dir, Manifest: m, Asset: other, Resume: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.RolledBack {
		t.Fatal("expected rollback")
	}
	if res.ResumeFrom != StageParse {
		t.Fatalf("resume from = %s", res.ResumeFrom)
	}
	if res.Reason == "" {
		t.Fatal("rollback has no reason")
	}
	// After rollback the run must redo every stage and produce output for the
	// new input, not the stale one.
	if res.FinalDigest == "" {
		t.Fatal("rollback did not complete the migration")
	}
}

func TestResumeMismatchedManifestVersionRollsBack(t *testing.T) {
	dir := t.TempDir()
	v1 := testManifest(t, manifestV1)
	if _, err := Run(Options{Out: dir, Manifest: v1, Asset: testAsset(), StopAfter: StageParse}); err != nil {
		t.Fatalf("partial: %v", err)
	}
	v2 := testManifest(t, `{"version": 2, "asset_id": "game-ep01", "audio": {}}`)
	res, err := Run(Options{Out: dir, Manifest: v2, Asset: testAsset(), Resume: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.RolledBack {
		t.Fatal("expected rollback on manifest version change")
	}
	if res.Reason == "" {
		t.Fatal("no rollback reason")
	}
}

func TestCorruptCheckpointStopsResume(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	if _, err := Run(Options{Out: dir, Manifest: m, Asset: testAsset(), StopAfter: StageParse}); err != nil {
		t.Fatalf("partial: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, checkpointDir))
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			target = filepath.Join(dir, checkpointDir, e.Name())
		}
	}
	if target == "" {
		t.Fatal("no checkpoint file")
	}
	if err := os.WriteFile(target, []byte(`{"stage":"parse","index":0,"summary_digest":"deadbeef"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Run(Options{Out: dir, Manifest: m, Asset: testAsset(), Resume: true})
	if err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
	if timeline.ExitCodeOf(err) != timeline.ExitCheckpoint {
		t.Fatalf("exit = %d, err = %v", timeline.ExitCodeOf(err), err)
	}
}

func TestWriteFailurePreservesPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(t, manifestV1)
	a := testAsset()
	first, err := Run(Options{Out: dir, Manifest: m, Asset: a})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Sabotage the next generation's staging by making the generation path a
	// file, which forces the final rename to fail.
	a2 := testAsset()
	a2.SHA256 = "second-input"
	gen, err := generationName(m.Digest(), a2, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, gen), []byte("blocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Run(Options{Out: dir, Manifest: m, Asset: a2, StopAfter: StageWrite})
	if err == nil {
		t.Fatal("write failure not reported")
	}
	// The committed pointer must still name the first generation.
	store, _ := NewStore(dir)
	data, cur, err := ReadCommitted(store)
	if err != nil {
		t.Fatalf("read committed: %v", err)
	}
	if cur.Generation != first.Generation {
		t.Fatalf("CURRENT moved to %s, want %s", cur.Generation, first.Generation)
	}
	if len(data) == 0 {
		t.Fatal("previous manifest lost")
	}
	// No staging directories may be left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > 9 && e.Name()[:9] == ".staging-" {
			t.Fatalf("staging dir leaked: %s", e.Name())
		}
	}
}

func TestAtomicWriteNeverLeavesPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value.json")
	if err := atomicWrite(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two\n" {
		t.Fatalf("content = %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files leaked: %d entries", len(entries))
	}
}

func TestNormalizeOutputIsByteStable(t *testing.T) {
	m := testManifest(t, manifestV1)
	n1, err := buildNormalized(m)
	if err != nil {
		t.Fatal(err)
	}
	m2 := testManifest(t, manifestV1)
	n2, err := buildNormalized(m2)
	if err != nil {
		t.Fatal(err)
	}
	b1 := mustJSON(t, n1)
	b2 := mustJSON(t, n2)
	if string(b1) != string(b2) {
		t.Fatalf("normalized output differs:\n%s\n%s", b1, b2)
	}
}

var tracksFixture = []tracks.Track{
	{Number: 1, Type: tracks.TypeVideo, CodecID: "V_MPEG4/ISO/AVC", Language: "jpn"},
	{Number: 2, Type: tracks.TypeAudio, CodecID: "A_OPUS", Language: "jpn"},
}
