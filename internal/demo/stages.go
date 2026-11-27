package demo

import (
	"context"
	"os"
	"path/filepath"

	"mediad/internal/derive"
	"mediad/internal/fixture"
	"mediad/internal/ingest"
	"mediad/internal/manifest"
	"mediad/internal/normalize"
	"mediad/internal/pipeline"
	"mediad/internal/quality"
)

// ingestStage re-hashes the immutable source and records the fingerprint.
func (p *Processor) ingestStage(s *fixture.Sample, fp *ingest.SourceFingerprint, ap assetPaths) pipeline.StageFunc {
	return func(ctx context.Context, _ *pipeline.State, _ string, workDir string) error {
		sum, size, stats, err := ingest.HashFile(ctx, ap.stream, p.cfg)
		if err != nil {
			return err
		}
		if err := ingest.Guard(stats, p.cfg); err != nil {
			return err
		}
		got := ingest.HexDigest(sum)
		if got != fp.StreamSHA {
			return modelDigestMismatch(fp.StreamSHA, got)
		}
		doc := map[string]any{
			"source_sha256": fp.SourceSHA,
			"stream_sha256": fp.StreamSHA,
			"index_sha256":  fp.IndexSHA,
			"stream_bytes":  size,
			"codec":         s.Codec,
		}
		return writeJSON(workDir, "fingerprint.json", doc)
	}
}

// normalizeStage scans boundaries, builds the canonical track and encodes a
// deterministic MP4 derivative input.
func (p *Processor) normalizeStage(s *fixture.Sample) pipeline.StageFunc {
	return func(_ context.Context, _ *pipeline.State, _ string, workDir string) error {
		sc := ingest.NewScanner(p.cfg)
		scan, err := sc.Scan(context.Background(), s.Codec, s.Stream)
		if err != nil {
			return err
		}
		track, err := normalize.Build(s.Codec, scan.Units, s.FPS,
			normalize.Config{Width: s.Width, Height: s.Height})
		if err != nil {
			return err
		}
		if err := writeJSON(workDir, "track.json", track); err != nil {
			return err
		}
		config := codecConfig(s.Codec)
		sampleData := samplePayload(s.Codec, s.Stream, scan.Units)
		mp4Bytes, err := normalize.EncodeMP4(track, config, sampleData)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(workDir, "track.mp4"), mp4Bytes, 0o644); err != nil {
			return err
		}
		return writeJSON(workDir, "digests.json", map[string]string{
			"track_sha256": sha256Hex(trackJSON(track)),
			"mp4_sha256":   sha256Hex(mp4Bytes),
		})
	}
}

// deriveStage computes quality metrics and writes the content-addressed
// derivative through the derive store.
func (p *Processor) deriveStage(s *fixture.Sample) pipeline.StageFunc {
	return func(_ context.Context, st *pipeline.State, _ string, workDir string) error {
		// Read the committed track from the normalize stage directory.
		track, err := p.readTrack(st.SourceSHA)
		if err != nil {
			return err
		}
		metrics, err := computeMetrics(s)
		if err != nil {
			return err
		}
		params := derive.Params{Kind: "quality-report", TargetBPS: s.TargetBPS, Version: 1}
		addr, err := derive.AddressFor(sha256Hex(trackJSON(track)), params)
		if err != nil {
			return err
		}
		report, err := writeJSONBytes(metrics)
		if err != nil {
			return err
		}
		if err := p.store.Put(addr, report); err != nil {
			return err
		}
		if err := writeJSON(workDir, "derivative.json", map[string]string{
			"derive_key":    addr.Key,
			"input_digest":  addr.InputDigest,
			"report_sha256": sha256Hex(report),
		}); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(workDir, "report.json"), report, 0o644)
	}
}

// manifestStage materializes the candidate manifest entry artifact.
func (p *Processor) manifestStage(fp *ingest.SourceFingerprint) pipeline.StageFunc {
	return func(_ context.Context, st *pipeline.State, _ string, workDir string) error {
		track, err := p.readTrack(st.SourceSHA)
		if err != nil {
			return err
		}
		mp4Digest, err := p.readDigest(st.SourceSHA, "mp4_sha256")
		if err != nil {
			return err
		}
		entry := &manifest.Entry{
			SourceSHA:     fp.SourceSHA,
			Codec:         st.Codec,
			StreamSHA:     fp.StreamSHA,
			IndexSHA:      fp.IndexSHA,
			StreamBytes:   fp.StreamBytes,
			Timescale:     track.Timescale,
			SampleCount:   len(track.Samples),
			KeyCount:      len(track.KeySampleNumbers()),
			StartNS:       track.StartNS,
			EndNS:         track.EndNS,
			MaxAlignErrNS: track.MaxAlignmentNS,
			TrackDigest:   sha256Hex(trackJSON(track)),
			MP4Digest:     mp4Digest,
		}
		return writeJSON(workDir, "entry.json", entry)
	}
}

// archiveStage is the final committed stage: it records the archive marker.
func (p *Processor) archiveStage(fp *ingest.SourceFingerprint) pipeline.StageFunc {
	return func(_ context.Context, _ *pipeline.State, _ string, workDir string) error {
		return writeJSON(workDir, "archive.json", map[string]string{
			"source_sha256": fp.SourceSHA,
			"status":        "committed",
		})
	}
}

// computeMetrics runs bitrate, PSNR and SSIM over the generated frames.
func computeMetrics(s *fixture.Sample) (map[string]any, error) {
	actualBPS, err := quality.Bitrate(int64(len(s.Stream)), s.DurationNS)
	if err != nil {
		return nil, err
	}
	w, h := int(s.Width), int(s.Height)
	psnrs := make([]float64, 0, len(s.Frames))
	ssims := make([]float64, 0, len(s.Frames))
	for i := range s.Frames {
		a := &quality.Frame{Width: w, Height: h, Stride: w, Pix: s.Frames[i]}
		b := &quality.Frame{Width: w, Height: h, Stride: w, Pix: s.DerivedFrames[i]}
		psnr, err := quality.PSNR(a, b)
		if err != nil {
			return nil, err
		}
		if psnr > quality.PSNRInfSentinel {
			psnr = quality.PSNRInfSentinel
		}
		psnrs = append(psnrs, psnr)
		sim, err := quality.SSIM(a, b)
		if err != nil {
			return nil, err
		}
		ssims = append(ssims, sim)
	}
	return map[string]any{
		"target_bps":        s.TargetBPS,
		"actual_bps":        actualBPS,
		"bitrate_error_pct": quality.BitrateErrorPct(actualBPS, s.TargetBPS),
		"psnr_db":           quality.Aggregate(psnrs),
		"ssim":              quality.Aggregate(ssims),
	}, nil
}
