package release_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"assetpack/internal/clock"
	"assetpack/internal/release"
)

func testManifest(version string, seq int, rgb release.RGB) *release.Manifest {
	return &release.Manifest{
		Version: version, Sequence: seq, TimebaseHz: clock.Hz,
		Range: clock.Range{From: 45000, To: 135000},
		Artifacts: []release.Artifact{
			{Path: "desktop/frame-00001.rgba", Kind: "frame", Platform: "desktop"},
			{Path: "audio/audio.pcm", Kind: "audio", Platform: "desktop"},
		},
		Targets: []release.Target{{
			Platform: "desktop", FrameCount: 1, AudioSampleFrames: 1,
			RGBMean: rgb, PeakDBFS: -12.041199826559248,
		}},
	}
}

func TestCommitVerifyRollbackIdempotent(t *testing.T) {
	root := t.TempDir()
	frame1 := []byte{10, 20, 30, 255}
	audio := []byte{0x00, 0x20} // int16 LE 8192
	m1 := testManifest("seq-1", 1, release.RGB{R: 10, G: 20, B: 30})
	res1, err := release.Commit(release.CommitRequest{Root: root, Manifest: m1, Files: map[string][]byte{
		"desktop/frame-00001.rgba": frame1,
		"audio/audio.pcm":          audio,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res1.State.Current != "seq-1" {
		t.Fatalf("state %+v", res1.State)
	}
	if _, _, err := release.Rollback(context.Background(), root, "previous", false); !errors.Is(err, release.ErrNoPrevious) || !errors.Is(err, release.ErrRollbackFailed) {
		t.Fatalf("first release rollback should be both no-previous and rollback failure, got %v", err)
	}

	frame2 := []byte{200, 100, 50, 255}
	m2 := testManifest("seq-2", 2, release.RGB{R: 200, G: 100, B: 50})
	if _, err := release.Commit(release.CommitRequest{Root: root, Manifest: m2, Files: map[string][]byte{
		"desktop/frame-00001.rgba": frame2,
		"audio/audio.pcm":          audio,
	}}); err != nil {
		t.Fatal(err)
	}
	st, _ := release.LoadState(root)
	if st.Previous != "seq-1" {
		t.Fatalf("previous=%s", st.Previous)
	}

	m, st, err := release.Rollback(context.Background(), root, "previous", true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Sequence != 1 || st.Current != "seq-1" {
		t.Fatalf("rollback m=%+v state=%+v", m, st)
	}
	got, err := os.ReadFile(filepath.Join(root, "desktop", "frame-00001.rgba"))
	if err != nil || string(got) != string(frame1) {
		t.Fatalf("stable desktop link did not restore seq1: %v", err)
	}

	_, st2, err := release.Rollback(context.Background(), root, "previous", true)
	if err != nil {
		t.Fatalf("repeat rollback must be rerunnable: %v", err)
	}
	if st2.Current != "seq-1" || st2.Previous != "seq-1" {
		t.Fatalf("idempotent rollback state=%+v", st2)
	}
}

func TestFailedCommitLeavesNoCurrent(t *testing.T) {
	root := t.TempDir()
	m := testManifest("bad", 9, release.RGB{})
	m.Artifacts[0].SHA256 = "definitely-wrong"
	_, err := release.Commit(release.CommitRequest{Root: root, Manifest: m, Files: map[string][]byte{
		"desktop/frame-00001.rgba": {1, 2, 3, 255},
		"audio/audio.pcm":          {0, 0},
	}})
	if err == nil {
		t.Fatal("hash mismatch must fail commit")
	}
	if _, err := os.Stat(filepath.Join(root, release.CurrentLink)); !os.IsNotExist(err) {
		t.Fatalf("current must not exist after failed commit, err=%v", err)
	}
	if _, _, err := release.Rollback(context.Background(), root, "previous", false); !errors.Is(err, release.ErrRollbackFailed) {
		t.Fatalf("rollback without state should be release failure, got %v", err)
	}
}
