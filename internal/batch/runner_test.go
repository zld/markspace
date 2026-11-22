package batch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"mq/internal/batch"
	"mq/internal/fixtures"
)

func runDemo(t *testing.T, dir string) *batch.Report {
	t.Helper()
	files, err := fixtures.WriteAll(dir)
	if err != nil {
		t.Fatalf("fixtures: %v", err)
	}
	return batch.Run(context.Background(), files, batch.Config{
		TargetBps:     8_000_000,
		QueueCapacity: 4,
		SlowConsumer:  time.Millisecond,
		MaxInputBytes: 64 << 20,
		MaxTracks:     8,
	})
}

func TestBatchClassificationAndEvidence(t *testing.T) {
	rep := runDemo(t, t.TempDir())
	if rep.FilesOK != 2 || rep.Recoverable != 3 || rep.Fatal != 2 {
		t.Fatalf("counts ok=%d rec=%d fatal=%d", rep.FilesOK, rep.Recoverable, rep.Fatal)
	}
	if rep.Frames != 48 || rep.ManifestEntries != 48 {
		t.Fatalf("frames=%d entries=%d", rep.Frames, rep.ManifestEntries)
	}
	if rep.BitrateDeltaPct < -1.0 || rep.BitrateDeltaPct > 1.0 {
		t.Fatalf("bitrate delta=%v exceeds 1.0%%", rep.BitrateDeltaPct)
	}
	if rep.MaxAlignMs > 1.0 {
		t.Fatalf("max align=%v exceeds 1ms", rep.MaxAlignMs)
	}
	if rep.PeakQueue > 4 {
		t.Fatalf("peak=%d exceeds queue capacity", rep.PeakQueue)
	}
	if rep.QualityMean <= 0 || rep.QualityMin <= 0 {
		t.Fatalf("quality mean=%v min=%v", rep.QualityMean, rep.QualityMin)
	}
	// 损坏素材不得污染同批其他素材：两个良好素材均分类为 ok。
	var okFiles []string
	for _, a := range rep.Assets {
		if a.Class == batch.ClassOK {
			okFiles = append(okFiles, a.File)
		}
	}
	if len(okFiles) != 2 ||
		(!strings.HasSuffix(okFiles[0], "good.mxf") && !strings.HasSuffix(okFiles[1], "good.mxf")) {
		t.Fatalf("ok files=%v", okFiles)
	}
	// 前三类故障须携带原始偏移（offset= 出现在错误文本中）。
	for _, a := range rep.Assets {
		if a.Class == batch.ClassRecoverable && !strings.Contains(a.Err, "offset=") {
			t.Fatalf("recoverable error lacks offset: %s", a.Err)
		}
	}
}

func TestBatchRerunIdenticalHash(t *testing.T) {
	a := runDemo(t, t.TempDir())
	b := runDemo(t, t.TempDir())
	if a.FrameHash != b.FrameHash {
		t.Fatalf("frame hash differs across runs:\n%s\n%s", a.FrameHash, b.FrameHash)
	}
	if a.Manifest != b.Manifest {
		t.Fatalf("manifest differs across runs")
	}
	if a.ActualBps != b.ActualBps || a.Frames != b.Frames {
		t.Fatalf("stats differ across runs")
	}
}
