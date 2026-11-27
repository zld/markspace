package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/manifest"
	"github.com/zld/continuity/internal/mux"
	"github.com/zld/continuity/internal/pipeline"
	"github.com/zld/continuity/internal/repair"
	"github.com/zld/continuity/internal/report"
)

// fixturePath and checkpointDir are fixed names inside the work directory so a
// repeated invocation resumes against exactly the same on-disk state.
const (
	fixtureName = "source.mp4"
	cpDirName   = "checkpoints"
)

func runDemo(args []string) int {
	d, err := parseDemoFlags(args)
	if err != nil {
		return exitInternal
	}
	if err := os.MkdirAll(d.work, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "work dir: %v\n", err)
		return exitInternal
	}
	srcPath := filepath.Join(d.work, fixtureName)
	cpDir := filepath.Join(d.work, cpDirName)

	// A corrupt case writes a deliberately damaged sample table that preserves
	// box framing but drops trailing size rows, so the demuxer can name both the
	// track and the first out-of-range sample instead of only reporting EOF.
	fault := mux.FaultNone
	if d.caseName == "corrupt" {
		fault = mux.FaultTruncateStsz
	}
	if err := writeDemoFixture(srcPath, d, fault); err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		return exitInternal
	}

	fp, err := index.FingerprintFile(srcPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fingerprint: %v\n", err)
		return exitInternal
	}

	in, err := demux.Open(srcPath)
	if err != nil {
		// Corrupt input must report the track/sample location and exit 2.
		fmt.Fprintf(os.Stderr, "demux rejected input: %v\n", err)
		return mapExit(err)
	}

	summary, code := process(d, in, fp, cpDir)
	if err := in.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close: %v\n", err)
		return exitInternal
	}
	if code != exitOK {
		return code
	}

	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal summary: %v\n", err)
		return exitInternal
	}
	fmt.Println(string(raw))
	return exitOK
}

// process runs repair + indexing, then assembles the verification summary. It
// returns a non-zero code on interrupt/resume failures while still closing the
// caller-owned demux handle.
func process(d demoFlags, in *demux.File, fp index.Fingerprint, cpDir string) (report.Summary, int) {
	policy := clock.DefaultPolicy()
	cfg := pipeline.Config{
		ChunkSize:      d.chunk,
		ReadQueue:      d.chunk,
		PendingWrites:  4,
		SlowDelay:      time.Duration(d.slowMS) * time.Millisecond,
		InterruptAfter: d.interruptAfter,
	}

	out, err := pipeline.RunWithResume(context.Background(), in, policy, cpDir, fp, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: %v\n", err)
		return report.Summary{}, mapExit(err)
	}

	// Re-run repair purely to count rewrites and build the manifest against the
	// authoritative repaired timeline; this is metadata work, not a second pass
	// over media bytes.
	repaired, err := repair.Track(in, policy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "repair audit: %v\n", err)
		return report.Summary{}, mapExit(err)
	}

	manifestRes := alignManifest(repaired)
	bitrate := demoBitrate(in)
	psnr := demoPSNR(in)

	summary := report.Build(report.Inputs{
		TargetBitrate:      bitrate.target,
		ActualBitrate:      bitrate.actual,
		PSNRDB:             psnr,
		RepairedTimestamps: repaired.RepairedCount(),
		PeakQueueChunks:    out.Metrics.PeakQueueChunks,
		CheckpointResumed:  out.ResumedBlocks > 0,
		ResumedBlocks:      out.ResumedBlocks,
		Index:              out.Index,
		Manifest:           manifestRes,
	})
	return summary, exitOK
}

// alignManifest builds a schedule from every sync sample of the first video
// track and aligns it on the shared rational time base. Audio-only fixtures are
// skipped by returning an empty result rather than failing.
func alignManifest(repaired *repair.Result) manifest.Result {
	for _, rt := range repaired.Tracks {
		if rt.Video {
			items := manifest.FromTrack(rt, 1)
			res, err := manifest.Align(items, rt)
			if err != nil {
				return manifest.Result{}
			}
			return res
		}
	}
	return manifest.Result{}
}
