package pipeline

import (
	"os"
	"path/filepath"

	"medpipe/internal/clock"
	"medpipe/internal/media"
)

func (r *Runner) buildResult(manifestSHA, outputSHA string) *Result {
	r.state.Stats.Duplicates = r.reorder.Stats.Duplicates
	r.state.Stats.Missing = len(r.reorder.Missing)
	r.state.Stats.OutOfOrder = r.reorder.Stats.OutOfOrder
	r.state.Stats.ShortSilenceRuns = r.silence.ShortRuns()

	segments := append([]media.Segment{}, r.state.Segments...)
	silence := append([]media.SilenceRange{}, r.state.SilenceRanges...)
	stats := r.pipe.Stats()
	res := &Result{
		Segments: segments, SilenceRanges: silence,
		Duplicates: r.reorder.Stats.Duplicates, Missing: len(r.reorder.Missing),
		QueuePeakBytes: r.pipe.PeakBytes(), QueueLimitBytes: r.opts.QueueBytes,
		BackpressureWaits: r.pipe.Budget().WaitCount(),
		ManifestSHA256:    manifestSHA, OutputSHA256: outputSHA,
		ManifestPath: manifestPath(r.opts.OutDir), CheckpointPath: checkpointPath(r.opts.OutDir),
		PipeStats: &stats,
	}
	if r.state.FirstError != nil {
		res.ErrorCode = r.state.FirstError.Code
		res.ExitCode = 2
		res.Status = "completed_with_located_input_errors"
		if loc := r.state.FirstError.Location; loc != nil {
			res.CorruptPacketSeq = loc.Sequence
			res.CorruptByteOffset = loc.ByteOffset
		}
	} else {
		res.ExitCode = 0
		res.Status = "completed_verified"
	}
	return res
}

func (r *Runner) completeOutputs() (*Result, error) {
	r.state.ensureSlices()
	r.state.Stats.SourceChunks = r.state.ProcessedChunks
	r.state.Stats.Duplicates = r.reorder.Stats.Duplicates
	r.state.Stats.Missing = len(r.reorder.Missing)
	r.state.Stats.OutOfOrder = r.reorder.Stats.OutOfOrder
	r.state.Stats.ShortSilenceRuns = r.silence.ShortRuns()

	manifest := Manifest{
		Format: FormatManifest, ParserMajor: ParserMajor, InputSHA256: r.inputDigest,
		TimeUnit: "nanoseconds", SampleRate: r.cfg.SampleRate,
		QueueLimitBytes: r.opts.QueueBytes,
		Segments:        r.state.Segments, SilenceRanges: r.state.SilenceRanges,
		Stats: r.state.Stats, ErrorCode: r.state.ErrorCode,
	}
	if r.state.FirstError != nil {
		manifest.Corruption = r.state.FirstError.Location
		manifest.ErrorCode = r.state.FirstError.Code
	}

	manifestSHA, _, err := writeManifestFile(manifestPath(r.opts.OutDir), manifest)
	if err != nil {
		return nil, err
	}
	outputSHA, err := writeChecksumsFile(checksumPath(r.opts.OutDir), r.state.Segments, manifestSHA)
	if err != nil {
		return nil, err
	}
	if _, _, err := VerifyChecksumFile(r.opts.OutDir); err != nil {
		return nil, err
	}
	if err := r.saveCheckpoint(true, outputSHA); err != nil {
		return nil, err
	}
	res := r.buildResult(manifestSHA, outputSHA)
	return res, nil
}

// VerifyChecksumFile verifies every relative file listed in checksums.sha256
// and returns the listed manifest hash plus the hash of the checksum file.
func VerifyChecksumFile(outDir string) (string, string, error) {
	path := checksumPath(outDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", clock.Wrap(clock.CodeCheckpointInvalid, nil, err, "read checksums")
	}
	var manifestSHA string
	lines := splitChecksumLines(string(b))
	for _, line := range lines {
		fields := splitChecksumLine(line)
		if len(fields) != 2 {
			continue
		}
		want, rel := fields[0], fields[1]
		if rel == ManifestName {
			manifestSHA = want
		}
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) || clean == ".." || len(clean) >= 2 && clean[0:2] == ".." {
			return "", "", clock.Errorf(clock.CodeInvalidPacket, nil, "unsafe checksum path %q", rel)
		}
		data, err := os.ReadFile(filepath.Join(outDir, clean))
		if err != nil {
			return "", "", clock.Wrap(clock.CodeCheckpointInvalid, nil, err, "read checksum target %s", rel)
		}
		if got := sha256Hex(data); got != want {
			return "", "", clock.Errorf(clock.CodeCheckpointInvalid, nil,
				"checksum mismatch for %s: want %s got %s", rel, want, got)
		}
	}
	return manifestSHA, sha256Hex(b), nil
}

func splitChecksumLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func splitChecksumLine(line string) []string {
	for i := 0; i+2 < len(line); i++ {
		if line[i] == ' ' && line[i+1] == ' ' {
			return []string{line[:i], line[i+2:]}
		}
	}
	return nil
}
