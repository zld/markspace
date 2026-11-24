package checkpoint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/mediapipe/checkpoint"
	"github.com/zld/mediapipe/media"
)

func opts(seg media.Time) checkpoint.Options {
	return checkpoint.Options{
		InputSHA256: "aaaa", IndexDigest: "iiii", ParamsDigest: "pppp", Segment: seg,
	}
}

func TestCommitIsIdempotent(t *testing.T) {
	s := checkpoint.Create(opts(2 * media.Time(media.NanoDen)))
	seg := checkpoint.Segment{Number: 1, Layer: "reference", Path: "segments/reference-seg001.bin", SHA256: "abc", Bytes: 10}
	if err := s.Commit(seg); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(seg); err != nil {
		t.Fatal(err)
	}
	if len(s.Segments) != 1 {
		t.Fatalf("segments=%d want 1", len(s.Segments))
	}
}

func TestCommitRejectsConflictingDigest(t *testing.T) {
	s := checkpoint.Create(opts(2 * media.Time(media.NanoDen)))
	seg := checkpoint.Segment{Number: 1, Layer: "reference", Path: "x", SHA256: "abc"}
	_ = s.Commit(seg)
	seg.SHA256 = "different"
	if err := s.Commit(seg); err == nil {
		t.Fatal("conflicting digest should fail")
	}
}

func TestValidateRejectsMismatchedInput(t *testing.T) {
	s := checkpoint.Create(opts(2 * media.Time(media.NanoDen)))
	other := opts(2 * media.Time(media.NanoDen))
	other.InputSHA256 = "bbbb"
	if err := s.Validate(other); err == nil {
		t.Fatal("input change must invalidate checkpoint")
	} else if !media.IsKind(err, media.KindCheckpoint) {
		t.Fatalf("kind=%v", media.Category(err))
	}
}

func TestValidateRejectsMismatchedParams(t *testing.T) {
	s := checkpoint.Create(opts(2 * media.Time(media.NanoDen)))
	other := opts(3 * media.Time(media.NanoDen))
	if err := s.Validate(other); err == nil {
		t.Fatal("segment duration change must invalidate checkpoint")
	}
	other = opts(2 * media.Time(media.NanoDen))
	other.ParamsDigest = "qqqq"
	if err := s.Validate(other); err == nil {
		t.Fatal("params change must invalidate checkpoint")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := checkpoint.Create(opts(2 * media.Time(media.NanoDen)))
	// Write a real segment file so Load keeps it.
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	path := filepath.Join(dir, "segments", "reference-seg001.bin")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _, err := media.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(checkpoint.Segment{Number: 1, Layer: "reference",
		Path: "segments/reference-seg001.bin", SHA256: sum, Bytes: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.Save(dir, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := checkpoint.Load(dir, opts(2*media.Time(media.NanoDen)))
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || len(loaded.Segments) != 1 {
		t.Fatalf("loaded=%+v", loaded)
	}
	if !loaded.Has("reference", 1) {
		t.Fatal("committed segment lost")
	}
}

func TestLoadDropsMissingSegmentFile(t *testing.T) {
	dir := t.TempDir()
	s := checkpoint.Create(opts(2 * media.Time(media.NanoDen)))
	if err := s.Commit(checkpoint.Segment{Number: 1, Layer: "reference",
		Path: "segments/missing.bin", SHA256: "abc"}); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.Save(dir, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := checkpoint.Load(dir, opts(2*media.Time(media.NanoDen)))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Segments) != 0 {
		t.Fatal("missing file must not count as committed")
	}
}

func TestLoadDropsAlteredSegmentFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "segments", "s.bin")
	_ = os.WriteFile(path, []byte("original"), 0o644)
	s := checkpoint.Create(opts(2 * media.Time(media.NanoDen)))
	_ = s.Commit(checkpoint.Segment{Number: 1, Layer: "reference", Path: "segments/s.bin", SHA256: "WRONG"})
	_ = checkpoint.Save(dir, s)
	loaded, err := checkpoint.Load(dir, opts(2*media.Time(media.NanoDen)))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Segments) != 0 {
		t.Fatal("altered file must not count as committed")
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(checkpoint.Path(dir), []byte("{not json"), 0o644)
	if _, err := checkpoint.Load(dir, opts(2*media.Time(media.NanoDen))); err == nil {
		t.Fatal("corrupt checkpoint should be a classified error")
	}
}

func TestLoadMissingIsNotError(t *testing.T) {
	dir := t.TempDir()
	loaded, err := checkpoint.Load(dir, opts(2*media.Time(media.NanoDen)))
	if err != nil {
		t.Fatal(err)
	}
	if loaded != nil {
		t.Fatal("expected nil state")
	}
}
