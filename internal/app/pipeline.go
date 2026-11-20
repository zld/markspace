// Package app implements the hlscheck command pipeline: fixture generation,
// slicing, playlist publishing, validation and fault injection.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"hlscheck/internal/fixture"
	"hlscheck/internal/fsutil"
	"hlscheck/internal/hls"
	"hlscheck/internal/model"
	"hlscheck/internal/slicer"
	"hlscheck/internal/validate"
)

// DemoConfig holds parsed arguments for the demo pipeline.
type DemoConfig struct {
	WorkDir   string
	Duration  time.Duration
	Rates     []int
	Target    time.Duration
	FrameRate int
}

// PipelineResult is the demo output consumed by the terminal renderer.
type PipelineResult struct {
	Index   *hls.Index
	Report  *validate.Report
	Root    string
	Sources []string
}

// RunDemo generates fixtures, slices every rendition, writes playlists and
// validates the result. All output stays inside cfg.WorkDir.
func RunDemo(ctx context.Context, cfg DemoConfig) (*PipelineResult, error) {
	if err := fixture.VerifyFFmpeg(); err != nil {
		return nil, err
	}
	if len(cfg.Rates) == 0 {
		return nil, fmt.Errorf("at least one rendition bitrate is required")
	}
	rates := append([]int(nil), cfg.Rates...)
	sort.Ints(rates)

	srcDir, err := fsutil.SafeJoin(cfg.WorkDir, "src")
	if err != nil {
		return nil, err
	}
	hlsRoot, err := fsutil.SafeJoin(cfg.WorkDir, "hls")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(hlsRoot, 0o755); err != nil {
		return nil, err
	}

	gop := cfg.FrameRate * int(cfg.Target/time.Second)
	if gop <= 0 {
		gop = cfg.FrameRate
	}
	index := &hls.Index{FrameRate: cfg.FrameRate}
	var sources []string
	for _, bps := range rates {
		srcName := fmt.Sprintf("src_%d.ts", bps)
		srcPath, err := fsutil.SafeJoin(srcDir, srcName)
		if err != nil {
			return nil, err
		}
		spec := fixture.Spec{
			Bps: bps, Duration: cfg.Duration, FrameRate: cfg.FrameRate,
			GOP: gop, OutPath: srcPath,
		}
		if err := fixture.Generate(ctx, spec); err != nil {
			return nil, err
		}
		sources = append(sources, srcPath)

		dir := fmt.Sprintf("rendition_%d", bps)
		r := model.Rendition{Bps: bps, Source: srcPath, Dir: dir}
		result, err := slicer.Slice(hlsRoot, r, slicer.Options{
			Target:         cfg.Target,
			AlignTolerance: model.FrameTolerance,
			SegmentPrefix:  "seg_",
		})
		if err != nil {
			return nil, err
		}
		index.Renditions = append(index.Renditions, hls.IndexRendition{
			Rendition: r,
			Segments:  result.Segments,
			Playlist:  filepath.Join(dir, "playlist.m3u8"),
		})
	}

	// Target duration is the ceiling of the longest segment across variants.
	maxTarget := 0
	for _, ir := range index.Renditions {
		if t := hls.TargetDuration(ir.Segments); t > maxTarget {
			maxTarget = t
		}
	}
	index.TargetSeconds = maxTarget
	if err := hls.WriteAll(hlsRoot, index); err != nil {
		return nil, err
	}

	// Cross-rendition alignment findings from analysis are folded into the
	// validation report alongside the file-level checks.
	report := validate.Run(hlsRoot, index)
	return &PipelineResult{Index: index, Report: report, Root: hlsRoot, Sources: sources}, nil
}

// RunValidate loads the index and runs the full checker.
func RunValidate(root, indexPath string) (*validate.Report, *hls.Index, error) {
	idx, err := validate.LoadIndex(root, indexPath)
	if err != nil {
		return nil, nil, err
	}
	return validate.Run(root, idx), idx, nil
}

// RunFault applies deterministic drop/swap operations to an existing tree.
func RunFault(root, indexPath string, drops []validate.DropRequest, swaps []validate.SwapRequest) ([]string, []string, error) {
	idx, err := validate.LoadIndex(root, indexPath)
	if err != nil {
		return nil, nil, err
	}
	dropped, err := validate.ApplyDrops(root, idx, drops)
	if err != nil {
		return dropped, nil, err
	}
	swapped, err := validate.ApplySwaps(root, idx, swaps)
	return dropped, swapped, err
}
