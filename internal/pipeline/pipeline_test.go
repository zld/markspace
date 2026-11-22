package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediaarchive/internal/fixtures"
)

func generated(t *testing.T, set string) []AssetInput {
	t.Helper()
	dir := t.TempDir()
	fs, err := fixtures.Generate(dir, set)
	if err != nil {
		t.Fatal(err)
	}
	var out []AssetInput
	for _, a := range fs.Assets {
		out = append(out, AssetInput{
			Asset: a.Name, MP4: a.MP4, Subtitle: a.Subtitle, RTP: a.RTP,
			FPS: a.FPS, TargetBps: a.TargetBps, LabelFormat: a.LabelFormat,
		})
	}
	return out
}

func TestNormalFixtureStrictPassesAndReportIsStable(t *testing.T) {
	assets := generated(t, fixtures.SetNormal)
	opts := Options{Mode: ModeStrict, Workers: 4, Queue: 8, FPS: "30000/1001"}
	m1, err := Run(context.Background(), opts, assets)
	if err != nil {
		t.Fatalf("strict run: %v", err)
	}
	if m1.Metrics.CueCount < 10 {
		t.Fatalf("cue count = %d", m1.Metrics.CueCount)
	}
	if m1.Metrics.DamageCount != 0 {
		t.Fatalf("damage = %d", m1.Metrics.DamageCount)
	}
	if err := ValidateManifest(m1); err != nil {
		t.Fatal(err)
	}
	m2, err := Run(context.Background(), opts, assets)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Digest != m2.Digest {
		t.Fatalf("digest changed: %s vs %s", m1.Digest, m2.Digest)
	}
	if len(m1.Digest) != 64 {
		t.Fatalf("digest = %q", m1.Digest)
	}
}

func TestRecoverModeKeepsFaultsAndManifest(t *testing.T) {
	assets := generated(t, fixtures.SetDamaged)
	m, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 4, Queue: 8, FPS: "25"}, assets)
	if err != nil {
		t.Fatalf("recover run: %v", err)
	}
	if len(m.Faults) == 0 || m.Metrics.DamageCount == 0 {
		t.Fatalf("no faults recorded: %+v", m.Metrics)
	}
	if m.Digest == "" {
		t.Fatal("recover manifest without digest")
	}
	// Recovery must still produce rows from the healthy assets.
	if len(m.Rows) == 0 {
		t.Fatal("recover mode dropped every row")
	}
}

func TestStrictModeFailsWithOffset(t *testing.T) {
	assets := generated(t, fixtures.SetDamaged)
	m, err := Run(context.Background(), Options{Mode: ModeStrict, Workers: 2, Queue: 2, FPS: "25"}, assets)
	if err == nil {
		t.Fatal("strict mode accepted damaged fixtures")
	}
	if m.Digest != "" {
		t.Fatal("strict failure must not emit a manifest digest")
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Fatalf("error lacks offset: %v", err)
	}
}

func TestRepeatedRecoverRunsMatchDigest(t *testing.T) {
	assets := generated(t, fixtures.SetAll)
	opts := Options{Mode: ModeRecover, Workers: 4, Queue: 8, FPS: "30000/1001"}
	m1, err := Run(context.Background(), opts, assets)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m2, err := Run(context.Background(), opts, assets)
		if err != nil {
			t.Fatal(err)
		}
		if m1.Digest != m2.Digest {
			t.Fatalf("run %d digest drift: %s vs %s", i, m1.Digest, m2.Digest)
		}
		if len(m1.Rows) != len(m2.Rows) {
			t.Fatalf("row count drift: %d vs %d", len(m1.Rows), len(m2.Rows))
		}
	}
}

func TestCancelPropagatesThroughPipeline(t *testing.T) {
	assets := generated(t, fixtures.SetAll)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, err := Run(ctx, Options{Mode: ModeRecover, Workers: 4, Queue: 4, FPS: "30000/1001"}, assets)
	if err == nil {
		t.Fatal("canceled run reported success")
	}
	if m != nil && m.Digest != "" {
		t.Fatal("canceled run emitted a manifest digest")
	}
	if !errors.Is(err, context.Canceled) {
		if f, ok := asFault(err); !ok || f.Code != "canceled" {
			t.Fatalf("cancel error = %T %v", err, err)
		}
	}
}

func TestManifestDigestCoversSegments(t *testing.T) {
	assets := generated(t, fixtures.SetRTP)
	m, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 2, Queue: 2, FPS: "25"}, assets)
	if err != nil {
		t.Fatal(err)
	}
	if m.Metrics.DamageCount == 0 {
		t.Fatal("RTP loss fixture produced no damage")
	}
	base := Digest(m)
	m.Segments[0].EndMs += 1
	if Digest(m) == base {
		t.Fatal("digest ignores segment geometry")
	}
}

func TestInputFilesAreNotModified(t *testing.T) {
	assets := generated(t, fixtures.SetNormal)
	before := map[string]int64{}
	for _, a := range assets {
		for _, p := range []string{a.MP4, a.Subtitle} {
			if p == "" {
				continue
			}
			st, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			before[p] = st.Size()
		}
	}
	if _, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 2, Queue: 2, FPS: "30000/1001"}, assets); err != nil {
		t.Fatal(err)
	}
	for p, size := range before {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() != size {
			t.Fatalf("%s changed size %d -> %d", p, size, st.Size())
		}
		if filepath.Ext(p) == ".mp4" {
			// Read-only check: opening again must succeed.
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}
