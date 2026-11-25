package main

import (
	"fmt"
	"os"

	"github.com/zld/media-regress/fixture"
	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/manifest"
	"github.com/zld/media-regress/pipeline"
)

// loadInput reads a file or generates the selected fixture. The returned name
// is used for reporting and as the cache source identity.
// loadInput reads a file or generates the selected fixture. The second return
// is the reporting name, the third the 1-based segment the fixture asks to
// drop (0 for none).
func loadInput(f *flags) ([]byte, string, int, error) {
	if f.file != "" {
		data, err := os.ReadFile(f.file)
		if err != nil {
			return nil, "", 0, merr.Wrap(merr.StageFixture, merr.KindParse, 0, 0, err, "read --file")
		}
		return data, f.file, 0, nil
	}
	if f.truncateTS {
		f.fixture = fixture.NameBoundary
	}
	format := fixture.Format(f.format)
	if format != fixture.FormatMP4 && format != fixture.FormatMXF {
		return nil, "", 0, merr.New(merr.StageFixture, merr.KindParse, 0, 0, "unknown --format %q", f.format)
	}
	fx, err := fixture.Generate(fixture.Options{
		Name:         f.fixture,
		Format:       format,
		CorruptBlock: f.corruptBlock,
	})
	if err != nil {
		return nil, "", 0, err
	}
	drop := 0
	if fx.DropSegment {
		drop = 3
	}
	return fx.Bytes, fmt.Sprintf("%s.%s", fx.Name, fx.Format), drop, nil
}

// reportError prints the structured failure and selects the exit code. A
// manifest continuity or digest failure exits 3; everything else is input or
// processing error (2).
func reportError(res *pipeline.Result, err error) int {
	stage, kind, track, offset := "-", "-", "-", int64(0)
	if e, ok := merr.As(err); ok {
		stage, kind, track, offset = string(e.Stage), string(e.Kind), trackString(e.TrackID), e.InputOffset
	}
	fmt.Printf("failed stage=%s kind=%s track=%s input_offset=%d\n", stage, kind, track, offset)
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	if res != nil && res.Summary.Stages > 0 {
		fmt.Printf("stages_completed=%d\n", res.Summary.Stages)
	}
	if e, ok := merr.As(err); ok {
		fmt.Printf("track_id=%s input_offset=%d\n", trackString(e.TrackID), e.InputOffset)
	}
	// A timestamp fault is a first-class acceptance signal: report the count
	// and make explicit that no manifest was published.
	if e, ok := merr.As(err); ok && e.Kind == merr.KindTimestamp {
		fmt.Printf("timestamp_errors=1\n")
	}
	fmt.Printf("manifest_published=false\n")
	fmt.Printf("exit_code=%d\n", codeFor(err))
	return codeFor(err)
}

// codeFor selects the process exit code for a failure.
func codeFor(err error) int {
	if isManifestFailure(err) {
		return exitManifest
	}
	return exitInput
}

func trackString(id uint32) string {
	if id == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", id)
}

// isManifestFailure reports whether the failure is a final manifest
// continuity or digest failure (exit 3) rather than an input/processing error.
func isManifestFailure(err error) bool {
	if e, ok := merr.As(err); ok {
		return e.Stage == merr.StageManifest
	}
	return false
}

// reportSuccess prints the acceptance counters and verifies the manifest.
func reportSuccess(f *flags, res *pipeline.Result) int {
	m := res.Manifest
	// Verify before printing so continuity errors force a non-zero exit.
	errs := manifest.Verify(m, m.Track.Audio, true)
	if len(errs) != 0 {
		res.Summary.ContinuityErrors = countManifestErrors(errs)
		m.Summary = res.Summary
		_ = m.Finalize()
		fmt.Printf("stages=%d\n", res.Summary.Stages)
		fmt.Printf("queue_peak_bytes=%d\n", res.Summary.QueuePeakBytes)
		fmt.Printf("manifest_segments=%d\n", len(m.Track.Segments))
		fmt.Printf("continuity_errors=%d\n", res.Summary.ContinuityErrors)
		fmt.Printf("sha256=%s\n", m.Digest)
		fmt.Printf("cache_rejected=%d\n", res.Summary.CacheRejected)
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return exitManifest
	}

	if f.manifestPath != "" {
		data, err := m.Encode()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitManifest
		}
		if err := os.WriteFile(f.manifestPath, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitManifest
		}
	}

	printSuccess(f, res, m)
	return exitOK
}

func printSuccess(f *flags, res *pipeline.Result, m *manifest.Manifest) {
	s := res.Summary
	fmt.Printf("stages=%d\n", s.Stages)
	fmt.Printf("queue_peak_bytes=%d\n", s.QueuePeakBytes)
	fmt.Printf("queue_peak_blocks=%d\n", s.QueuePeakBlocks)
	fmt.Printf("manifest_segments=%d\n", len(m.Track.Segments))
	fmt.Printf("continuity_errors=%d\n", 0)
	fmt.Printf("timestamp_errors=%d\n", s.TimestampErrors)
	fmt.Printf("sha256=%s\n", m.Digest)
	fmt.Printf("manifest_sha256=%s\n", m.Digest)
	fmt.Printf("resume_block=%d\n", s.ResumeBlock)
	fmt.Printf("recomputed_blocks=%d\n", s.RecomputedBlocks)
	fmt.Printf("cache_rejected=%d\n", s.CacheRejected)
	fmt.Printf("conflicts=%d\n", s.Conflicts)
	fmt.Printf("truncated_blocks=%d\n", s.TruncatedBlocks)
	if res.CacheStats != nil {
		fmt.Printf("cache_hits=%d cache_misses=%d cache_stored=%d\n",
			res.CacheStats.Hits, res.CacheStats.Misses, res.CacheStats.Stored)
	}
	fmt.Printf("manifest_published=true\n")
	fmt.Printf("exit_code=0\n")
}

func countManifestErrors(errs []error) int {
	n := 0
	for _, err := range errs {
		if e, ok := merr.As(err); ok && e.Stage == merr.StageManifest {
			n++
		}
	}
	return n
}
