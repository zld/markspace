package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"streamcheck/internal/fixture"
)

func TestRejectsTraversalOutput(t *testing.T) {
	cfg := Config{
		OutDir: "../escaped", Duration: 6 * time.Second, Segment: 3 * time.Second,
		Tiers:       []fixture.Tier{{Name: "450k", BandwidthBPS: 450000}},
		Concurrency: 1, Faults: map[string]TierFault{},
	}
	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("traversal output path must be rejected")
	}
}

func TestNoTempFilesLeftBehind(t *testing.T) {
	cfg := Config{
		OutDir: t.TempDir() + "/out", Duration: 9 * time.Second, Segment: 3 * time.Second,
		Tiers:       []fixture.Tier{{Name: "450k", BandwidthBPS: 450000}},
		Concurrency: 1, Faults: map[string]TierFault{},
	}
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	filepath.WalkDir(cfg.OutDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".tmp-") || strings.HasPrefix(d.Name(), ".seg-") {
			t.Fatalf("temporary file left behind: %s", p)
		}
		return nil
	})
}
