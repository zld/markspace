package app_test

import (
	"context"
	"errors"
	"testing"

	"mediacore/internal/app"
	"mediacore/internal/diagnostic"
)

func TestDemoAndVerifyAcceptanceWorkflow(t *testing.T) {
	work := t.TempDir()
	ctx := context.Background()
	if err := app.Demo(ctx, work, 8); err != nil {
		t.Fatalf("demo: %v", err)
	}
	if err := app.Verify(ctx, work, 8, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}

	truncErr := app.Verify(ctx, work, 8, "truncated-shard")
	if !errors.Is(truncErr, diagnostic.ErrTruncated) || diagnostic.ExitCode(truncErr) != 2 {
		t.Fatalf("truncated direct case err=%v code=%d", truncErr, diagnostic.ExitCode(truncErr))
	}
	changedErr := app.Verify(ctx, work, 8, "changed-input")
	if !errors.Is(changedErr, diagnostic.ErrCheckpoint) || diagnostic.ExitCode(changedErr) != 3 {
		t.Fatalf("changed input direct case err=%v code=%d", changedErr, diagnostic.ExitCode(changedErr))
	}
}
