package segment_test

import (
	"path/filepath"
	"testing"

	"archivecheck/internal/fixture"
	"archivecheck/internal/model"
	mp4scan "archivecheck/internal/mp4"
	"archivecheck/internal/segment"
)

func TestFanoutPacketsAndBoundaryFanIn(t *testing.T) {
	dir := t.TempDir()
	paths, err := fixture.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := fixture.LoadManifest(paths.ValidInput)
	if err != nil {
		t.Fatal(err)
	}
	asset := m.Assets[0]
	asset.Path = filepath.Join(dir, asset.Path)
	scan, err := mp4scan.ScanAsset(asset)
	if err != nil {
		t.Fatal(err)
	}

	var reports []model.LayerReport
	for _, bitrate := range []int64{400_000, 900_000} {
		got, err := segment.BuildLayer(asset.ID, "record", bitrate, scan, asset.EditIntervals[0])
		if err != nil {
			t.Fatalf("BuildLayer(%d): %v", bitrate, err)
		}
		if len(got.Fragments) != segment.FragmentsPerLayer {
			t.Fatalf("fragments=%d want %d", len(got.Fragments), segment.FragmentsPerLayer)
		}
		if err := segment.VerifyPacketBoundaries(got.Fragments[1].Bytes); err != nil {
			t.Fatalf("RTP boundary: %v", err)
		}
		if got.Report.Recovery[1].ActualTick != 30_000 {
			t.Fatalf("actual recovery=%d want 30000", got.Report.Recovery[1].ActualTick)
		}
		reports = append(reports, got.Report)
	}

	max, err := segment.CheckLayerBoundaries(reports)
	if err != nil {
		t.Fatal(err)
	}
	if max != 0 {
		t.Fatalf("max boundary diff=%d want 0", max)
	}

	skewed := segment.IntroduceSkew(reports, 900_000, 1, 5)
	if _, err := segment.CheckLayerBoundaries(skewed); err == nil {
		t.Fatal("expected skewed layer boundary to fail")
	}
}
