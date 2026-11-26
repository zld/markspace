package media_test

import (
	"errors"
	"testing"

	"continuity/internal/fixture"
	"continuity/internal/media"
)

func TestMemoryLimitsAreEnforced(t *testing.T) {
	data := fixture.BuildMKV(fixture.MKVSpec{Seed: 17, Clusters: 1})
	_, err := media.ParseEBML(data, media.Limits{MaxElementSize: 16 << 20, MaxTotalBytes: 10})
	if !errors.Is(err, media.ErrMemoryBudget) {
		t.Fatalf("total budget err=%v", err)
	}

	bad := fixture.OutOfBoundsBlockSample()
	_, err = media.ParseEBML(bad.Data, media.Limits{MaxElementSize: 32, MaxTotalBytes: 64 << 20})
	if !errors.Is(err, media.ErrMemoryBudget) {
		t.Fatalf("element budget err=%v", err)
	}
}
