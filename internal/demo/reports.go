package demo

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"mediapipeline/internal/index"
	mp4media "mediapipeline/internal/mp4"
	"mediapipeline/pkg/media"
)

func printNormalMetrics(out io.Writer, cfg Config, r *PublishResult) {
	v := r.Verify
	fmt.Fprintf(out, "roundtrip=%.2f%% matched=%d/%d\n", v.RoundtripRate, v.FullSampleMatch, v.Samples)
	fmt.Fprintf(out, "keyframe_hit=%.2f%% matched=%d/%d\n", v.KeyframeHitRate, v.KeyframesMatched, v.KeyframesExpected)
	fmt.Fprintf(out, "segment_sha256=%.2f%% matched=%d/%d\n", v.SegmentHashRate, v.SegmentsMatched, v.Segments)
	fmt.Fprintf(out, "peak_chunks=%d workers=%d queue=%d bound=%d\n",
		r.PipeStats.PeakChunks, cfg.Workers, cfg.Queue, cfg.Workers+cfg.Queue)
}

func reportAnomalies(out io.Writer, cfg Config, files *InputFiles) bool {
	type item struct {
		class string
		path  string
	}
	items := []item{
		{media.ClassTruncatedMoov, files.TruncatedMoov},
		{media.ClassBadSTCO, files.BadSTCO},
		{media.ClassBackwardPTS, files.BackwardPTS},
	}
	corrupt := false
	for _, it := range items {
		info, err := mp4media.ScanFile(it.path, cfg.ChunkBytes)
		if err != nil {
			fmt.Fprintf(out, "fault class=%s file=%s fatal=%q\n", it.class, filepath.Base(it.path), err)
			corrupt = true
			continue
		}
		if info.Report.Corrupt() {
			corrupt = true
		}
		faults := info.Report.ByClass(it.class)
		if len(faults) == 0 && len(info.Report.Faults) > 0 {
			faults = info.Report.Faults
		}
		if len(faults) == 0 {
			fmt.Fprintf(out, "fault class=%s file=%s status=missing_classification\n", it.class, filepath.Base(it.path))
			continue
		}
		f := faults[0]
		fmt.Fprintf(out, "fault class=%s file=%s box=%s offset=%d tracks=%s scope=%s severity=%s\n",
			it.class, filepath.Base(it.path), f.Box, f.Offset, formatTracks(f), f.RecomputeScope, f.Severity)
	}
	return corrupt
}

func reportContainerOnly(out io.Writer, cfg Config, files *InputFiles, snapshots *index.SnapshotStore) (index.Decision, error) {
	baseInfo, err := mp4media.ScanFile(files.WrapBase, cfg.ChunkBytes)
	if err != nil {
		return index.Decision{}, err
	}
	changedInfo, err := mp4media.ScanFile(files.WrapChanged, cfg.ChunkBytes)
	if err != nil {
		return index.Decision{}, err
	}
	base := index.SnapshotFromInfo(baseInfo)
	changed := index.SnapshotFromInfo(changedInfo)
	d := index.Diff(&base, changed)
	_ = snapshots.Put(wrapBaseName, base)
	_ = snapshots.Put(wrapChangedName, changed)
	fmt.Fprintf(out, "change class=%s file=%s box=ftyp offset=12 tracks=1 scope=%s sample_recompute=%d container_only=%v\n",
		media.ClassContainerOnly, wrapChangedName, d.Scope, d.RecomputeSamples, d.ContainerOnly)
	return d, nil
}

func formatTracks(f *media.Fault) string {
	if f.TrackID != 0 {
		return fmt.Sprintf("%d", f.TrackID)
	}
	if len(f.TrackIDs) > 0 {
		parts := make([]string, 0, len(f.TrackIDs))
		for _, id := range f.TrackIDs {
			parts = append(parts, fmt.Sprintf("%d", id))
		}
		return strings.Join(parts, ",")
	}
	return "unknown"
}
