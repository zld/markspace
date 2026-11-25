package pipe_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/checkpoint"
	"mediacore/internal/diagnostic"
	"mediacore/internal/fixture"
	"mediacore/internal/manifest"
	"mediacore/internal/pipe"
	"mediacore/internal/provenance"
)

func generateRoot(t *testing.T) (string, *fixture.Bundle) {
	t.Helper()
	root := t.TempDir()
	b, err := fixture.GenerateAll(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	return root, b
}

func TestCheckpointResumeIsStableAndIdempotent(t *testing.T) {
	root, b := generateRoot(t)
	d, err := manifest.ParseDASH(b.NormalDASH, root)
	if err != nil {
		t.Fatal(err)
	}
	digest, _, err := provenance.CombinedInputDigest(b.NormalDASH, d.Entries)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pipe.DefaultConfig(filepath.Join(root, "run"))
	cfg.QueueCapacity = 2
	cfg.MaxReaders = 2

	first, err := pipe.ProcessEntries(context.Background(), d.Entries, digest, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Outcomes) != 6 || len(first.Completed) != 6 {
		t.Fatalf("outcomes=%d completed=%v", len(first.Outcomes), first.Completed)
	}
	if first.JobQueuePeak > cfg.QueueCapacity || first.ResultQueuePeak > cfg.QueueCapacity {
		t.Fatalf("queue peak exceeded capacity: job=%d result=%d cap=%d",
			first.JobQueuePeak, first.ResultQueuePeak, cfg.QueueCapacity)
	}
	type stable struct {
		digest string
		count  int
	}
	want := map[string]stable{}
	for _, o := range first.Outcomes {
		if o.Err != nil {
			t.Fatal(o.Err)
		}
		want[o.Entry.StableKey] = stable{o.Index.Digest, len(o.Index.Samples)}
	}

	second, err := pipe.ProcessEntries(context.Background(), d.Entries, digest, cfg)
	if err != nil {
		t.Fatal(err)
	}
	skipped := 0
	for _, o := range second.Outcomes {
		if !o.Skipped {
			t.Fatalf("entry %s was not reused from checkpoint", o.Entry.StableKey)
		}
		got := stable{o.Index.Digest, len(o.Index.Samples)}
		if want[o.Entry.StableKey] != got {
			t.Fatalf("unstable recovery for %s: first=%+v second=%+v", o.Entry.StableKey, want[o.Entry.StableKey], got)
		}
		skipped++
	}
	if skipped != 6 {
		t.Fatalf("skipped=%d", skipped)
	}

	_, loadErr := checkpoint.Load(checkpoint.Path(cfg.WorkDir), "changed-input", first.ConfigDigest)
	if !errors.Is(loadErr, diagnostic.ErrCheckpoint) {
		t.Fatalf("changed input checkpoint err=%v", loadErr)
	}
	if code := diagnostic.ExitCode(loadErr); code != diagnostic.ExitCheckpointInput {
		t.Fatalf("exit code=%d want 3", code)
	}
}

func TestCorruptionDoesNotAdvanceCheckpoint(t *testing.T) {
	root, b := generateRoot(t)
	c := b.Truncated
	d, err := manifest.ParseDASH(c.Manifest, root)
	if err != nil {
		t.Fatal(err)
	}
	digest, _, err := provenance.CombinedInputDigest(c.Manifest, d.Entries)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pipe.DefaultConfig(filepath.Join(root, "corrupt-run"))
	rep, err := pipe.ProcessEntries(context.Background(), d.Entries, digest, cfg)
	if !errors.Is(err, diagnostic.ErrTruncated) {
		t.Fatalf("process err=%v", err)
	}
	pos := diagnostic.PositionOf(err)
	if pos.Representation != c.Representation || pos.EntryIndex != c.EntryIndex || pos.ByteOffset != c.ByteOffset {
		t.Fatalf("bad corruption position: %+v want offset %d", pos, c.ByteOffset)
	}
	if len(rep.Completed) != 0 {
		t.Fatalf("checkpoint advanced: %v", rep.Completed)
	}
	if _, err := os.Stat(checkpoint.Path(cfg.WorkDir)); !os.IsNotExist(err) {
		t.Fatalf("checkpoint file should not exist, stat err=%v", err)
	}
}

func TestCanceledRunDoesNotAdvanceCheckpoint(t *testing.T) {
	root, b := generateRoot(t)
	d, err := manifest.ParseDASH(b.NormalDASH, root)
	if err != nil {
		t.Fatal(err)
	}
	digest, _, err := provenance.CombinedInputDigest(b.NormalDASH, d.Entries)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pipe.DefaultConfig(filepath.Join(root, "cancel-run"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := pipe.ProcessEntries(ctx, d.Entries, digest, cfg)
	if !errors.Is(err, diagnostic.ErrCancelled) {
		t.Fatalf("cancel err=%v", err)
	}
	if rep != nil && len(rep.Completed) != 0 {
		t.Fatalf("canceled run completed entries: %v", rep.Completed)
	}
	if _, err := os.Stat(checkpoint.Path(cfg.WorkDir)); !os.IsNotExist(err) {
		t.Fatalf("checkpoint exists after cancellation: %v", err)
	}
}
