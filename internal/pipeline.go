package medpipe

import (
	"fmt"
	"os"
	"path/filepath"
)

// RunTrack executes the full scan→index→normalize→stream chain for one track
// and returns its manifest section. Recoverable errors are collected inside
// the result; a fatal error is returned so the caller can mark the batch.
func RunTrack(spec TrackSpec, opts PipelineOptions) (TrackResult, error) {
	res := TrackResult{Track: spec.Track, Codec: spec.Codec, Kind: spec.Kind}
	capturePath := filepath.Join(opts.InputDir, spec.File)
	if perr := RequireLocalFile(capturePath); perr != nil {
		res.Status, res.Errors = StatusFailed, []*PipelineError{perr}
		return res, perr
	}
	digest, err := HashFile(capturePath)
	if err != nil {
		return res, err
	}
	res.InputDigest = digest

	// Reuse a digest-valid index; rebuild on any mismatch or missing file.
	idx, loadErr := LoadIndex(opts.WorkDir, spec.Track)
	if loadErr == nil && idx.IsValidFor(spec.Track, digest) {
		res.IndexRebuilt = false
	} else {
		// The digest no longer matches (or no index exists), so this is a
		// rebuild attempt even if the fresh scan subsequently fails: the old
		// index must never be silently treated as reusable for changed input.
		res.IndexRebuilt = true
		idx = nil
		scan, scanErr := scanTrack(capturePath, spec)
		res.Errors = append(res.Errors, scan.Errors...)
		if scanErr != nil {
			res.Status = StatusFailed
			return res, scanErr
		}
		built := BuildIndex(spec.Track, spec.Codec, spec.Kind, spec.ClockRate, digest, scan)
		idx = &built
		if err := idx.Save(opts.WorkDir); err != nil {
			return res, err
		}
	}
	res.FrameCount, res.KeyframeCount, res.DurationUS = idx.FrameCount, idx.KeyframeCount, idx.DurationUS
	res.ClockStats = idx.ClockStats

	// Metadata validation over the indexed frames (re-reading the capture).
	if spec.Kind == KindVideo {
		if report, vErr := validateColor(capturePath, spec, idx); vErr != nil {
			return res, vErr
		} else {
			res.Color = &report
			res.Errors = append(res.Errors, vErrList(spec.Track, report)...)
		}
	}
	if spec.Kind == KindAudio && spec.Audio != nil {
		res.Errors = append(res.Errors, ValidateAudio(spec.Track, spec.ClockRate,
			spec.Audio.ChannelCount(), spec.Audio.ChannelMap, *spec.Audio)...)
		if HasFatal(res.Errors) {
			res.Status = StatusFailed
			return res, res.Errors[0]
		}
	}

	// Streaming execution in bounded chunks, writing one MP4 fragment per chunk.
	exec, err := NewExecutor(opts.WorkDir, capturePath, idx, ExecuteOptions{
		Track: spec.Track, ChunkSize: opts.ChunkSize,
	})
	if err != nil {
		return res, err
	}
	defer exec.Close()
	sink := &fragmentSink{workDir: opts.WorkDir, spec: spec, idx: idx, resume: opts.Resume}
	if err := exec.Run(sink.visit); err != nil {
		res.Status = StatusFailed
		res.Errors = append(res.Errors, &PipelineError{
			Kind: KindFatal, Track: spec.Track, Code: "STREAM_INTERRUPTED",
			Message: err.Error(), Suggestion: "re-run with --resume to continue from the last keyframe",
		})
		return res, err
	}
	st := exec.Stats()
	res.Exec = &st
	if idx.KeyframeCount > 0 {
		if err := WriteTrackTFRA(opts.WorkDir, spec.Track, 1, idx); err != nil {
			return res, err
		}
	}
	return res, nil
}

// RunBatch processes every track and assembles the deterministic manifest.
// A fatal error on any track sets batch success false but does not abort the
// remaining tracks, so one bad input never hides the status of the others.
func RunBatch(specs []TrackSpec, opts PipelineOptions) (*Manifest, error) {
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, err
	}
	m := &Manifest{RunID: deriveRunID(specs, opts)}
	for _, spec := range specs {
		tr, _ := RunTrack(spec, opts)
		m.AppendTrack(tr)
	}
	m.Finalize(opts.TargetBPS)
	if _, err := m.Save(opts.WorkDir); err != nil {
		return m, err
	}
	return m, nil
}

// deriveRunID hashes the stable track/file/rate declarations so the same
// configured batch always carries the same run identifier.
func deriveRunID(specs []TrackSpec, opts PipelineOptions) string {
	h := newFixedHasher()
	for _, s := range specs {
		h.writeString(s.File)
		h.writeString(s.Track)
		h.writeString(string(s.Codec))
		h.writeUint32(s.ClockRate)
	}
	h.writeString(filepath.Clean(opts.InputDir))
	return h.hex12()
}

// scanTrack opens a capture and runs the streaming scanner.
func scanTrack(capturePath string, spec TrackSpec) (ScanResult, error) {
	f, err := os.Open(capturePath)
	if err != nil {
		return ScanResult{}, err
	}
	defer f.Close()
	s, magicErrs := NewCaptureScanner(f, ScanOptions{
		Track: spec.Track, Codec: spec.Codec, Kind: spec.Kind,
		ClockRate: spec.ClockRate, ExpectedDelta: spec.Delta,
	})
	if len(magicErrs) > 0 {
		return ScanResult{Errors: magicErrs}, magicErrs[0]
	}
	return s.Scan()
}

// validateColor re-reads frames and feeds their in-band color metadata to a
// first-known-wins validator.
func validateColor(capturePath string, spec TrackSpec, idx *TrackIndex) (ColorReport, error) {
	fr, err := NewFrameReader(capturePath, idx, spec.Track)
	if err != nil {
		return ColorReport{}, err
	}
	defer fr.Close()
	v := NewColorValidator(spec.Track)
	for ord := 0; ord < idx.FrameCount; ord++ {
		if err := fr.SeekFrame(ord); err != nil {
			return ColorReport{}, err
		}
		chunk, err := fr.ReadChunk(1)
		if err != nil || len(chunk) != 1 {
			return ColorReport{}, err
		}
		if chunk[0].HasMeta {
			v.Observe(chunk[0].Meta, chunk[0].ByteOffset)
		}
	}
	report, _ := v.Finalize()
	return report, nil
}

func vErrList(track string, r ColorReport) []*PipelineError {
	errs := make([]*PipelineError, 0, len(r.Conflicts)+len(r.Unknown))
	b := NewErrorBuilder(track)
	for _, c := range r.Conflicts {
		errs = append(errs, b.RecoverableAt(CodeColorConflict,
			fmt.Sprintf("%s: source=%d adopted=%d", c.Field, c.SourceVal, c.AdoptedVal),
			"verify encoders; first-known value retained", c.Offset))
	}
	for _, u := range r.Unknown {
		errs = append(errs, b.RecoverableAt(CodeColorUnknown,
			fmt.Sprintf("%s: unknown CICP %d ignored", u.Field, u.Value),
			"tag with a supported CICP value", u.Offset))
	}
	return errs
}
