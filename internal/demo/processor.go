// Package demo orchestrates the library subsystems into the end-to-end
// acceptance workflow: fixture generation, bounded ingest, normalization,
// derivation, staged archival, corruption handling and restore. The command
// entry point only parses flags and calls this package.
package demo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediad/internal/derive"
	"mediad/internal/fixture"
	"mediad/internal/ingest"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/pipeline"
)

// BufferConfig defaults: 64 KiB chunks, queue depth 4 => 5*64 KiB peak.
const (
	defaultChunkSize  = ingest.DefaultChunkSize
	defaultQueueDepth = 4
)

// Row is one acceptance line, field names matching the required output.
type Row struct {
	SourceSHA        string  `json:"source_sha256"`
	Codec            string  `json:"codec"`
	TargetBPS        int64   `json:"target_bps"`
	ActualBPS        int64   `json:"actual_bps"`
	BitrateErrorPct  float64 `json:"bitrate_error_pct"`
	PSNRDB           float64 `json:"psnr_db"`
	SSIM             float64 `json:"ssim"`
	ManifestStartNS  int64   `json:"manifest_start_ns"`
	ManifestEndNS    int64   `json:"manifest_end_ns"`
	AlignmentErrorNS int64   `json:"alignment_error_ns"`
	BufferPeakBytes  int64   `json:"buffer_peak_bytes"`
	Stage            string  `json:"stage"`
	ExitCode         int     `json:"exit_code"`
	// ErrorCode/ErrorOffset are populated for corrupt samples. Offset 0 is a
	// valid byte position, so ErrorOffset is always emitted (no omitempty).
	ErrorCode   string `json:"error_code,omitempty"`
	ErrorOffset int64  `json:"error_offset"`
	Case        string `json:"case"`
}

// Processor runs one asset through every stage. It owns the working directory
// layout and the shared manifest/derivative stores.
type Processor struct {
	work         string
	cfg          ingest.ReaderConfig
	engine       *pipeline.Engine
	store        *derive.Store
	manifest     *manifest.Manifest
	manifestPath string
}

// NewProcessor creates all working directories under workRoot.
func NewProcessor(workRoot string) (*Processor, error) {
	eng, err := pipeline.NewEngine(filepath.Join(workRoot, "pipeline"))
	if err != nil {
		return nil, err
	}
	store, err := derive.NewStore(filepath.Join(workRoot, "derive"))
	if err != nil {
		return nil, err
	}
	mfPath := filepath.Join(workRoot, "archive", "manifest.json")
	mf := manifest.New()
	if data, err := os.ReadFile(mfPath); err == nil {
		if mf, err = manifest.LoadBytes(data); err != nil {
			return nil, err
		}
	}
	return &Processor{
		work: workRoot, cfg: ingest.ReaderConfig{
			ChunkSize: defaultChunkSize, QueueDepth: defaultQueueDepth,
		},
		engine: eng, store: store, manifest: mf, manifestPath: mfPath,
	}, nil
}

// Config exposes the bounded read configuration for reporting.
func (p *Processor) Config() ingest.ReaderConfig { return p.cfg }

// PeakBytes is the configured buffering ceiling.
func (p *Processor) PeakBytes() int { return p.cfg.PeakBytes() }

// assetPaths groups the on-disk locations of one source.
type assetPaths struct {
	sourceDir string
	stream    string
}

func (p *Processor) paths(sourceSHA string) assetPaths {
	dir := filepath.Join(p.work, "sources", sourceSHA)
	return assetPaths{sourceDir: dir, stream: filepath.Join(dir, "stream.bin")}
}

// writeSource persists the immutable bitstream, then hashes it through the
// bounded chunk reader.
func (p *Processor) writeSource(sourceSHA string, stream []byte) (assetPaths, *ingest.Stats, error) {
	ap := p.paths(sourceSHA)
	if err := os.MkdirAll(ap.sourceDir, 0o755); err != nil {
		return ap, nil, err
	}
	if err := os.WriteFile(ap.stream, stream, 0o644); err != nil {
		return ap, nil, err
	}
	_, _, stats, err := ingest.HashFile(context.Background(), ap.stream, p.cfg)
	return ap, stats, err
}

// ProcessSuccess runs a valid fixture asset end to end and returns its
// acceptance row. Repeated calls for the same source reuse the committed
// result and never append a second manifest entry.
func (p *Processor) ProcessSuccess(ctx context.Context, s *fixture.Sample) (*Row, error) {
	// The index is validated before anything else trusts it.
	idx, err := ingest.ValidateIndex(s.Codec, s.Index.Entries, int64(len(s.Stream)))
	if err != nil {
		return nil, err
	}
	fp, err := ingest.FingerprintBytes(s.Codec, s.Stream, idx)
	if err != nil {
		return nil, err
	}
	ap, stats, err := p.writeSource(fp.SourceSHA, s.Stream)
	if err != nil {
		return nil, err
	}
	if err := ingest.Guard(stats, p.cfg); err != nil {
		return nil, err
	}

	// Duplicate delivery: reuse the archived result without re-appending.
	if p.manifest.Has(fp.SourceSHA) {
		return p.rowFromArchive(fp.SourceSHA, s, stats, pipeline.StageArchive), nil
	}

	runner := pipeline.NewRunner(p.engine, p.stageFuncs(s, fp, ap))
	st, err := runner.Run(ctx, fp.SourceSHA, s.Codec)
	if err != nil {
		return nil, err
	}

	// Aggregate the committed per-source entry into the global manifest.
	entry, err := p.loadEntryArtifact(st, fp.SourceSHA)
	if err != nil {
		return nil, err
	}
	if err := p.manifest.Add(entry); err != nil {
		return nil, err
	}
	if _, err := manifest.WriteAtomically(filepath.Dir(p.manifestPath),
		filepath.Base(p.manifestPath), p.manifest); err != nil {
		return nil, err
	}
	return p.rowFromArchive(fp.SourceSHA, s, stats, st.LastCommitted()), nil
}

// stageFuncs builds the five stage functions for one asset.
func (p *Processor) stageFuncs(s *fixture.Sample, fp *ingest.SourceFingerprint, ap assetPaths) map[string]pipeline.StageFunc {
	return map[string]pipeline.StageFunc{
		pipeline.StageIngest:    p.ingestStage(s, fp, ap),
		pipeline.StageNormalize: p.normalizeStage(s),
		pipeline.StageDerive:    p.deriveStage(s),
		pipeline.StageManifest:  p.manifestStage(fp),
		pipeline.StageArchive:   p.archiveStage(fp),
	}
}

// writeJSON is a small deterministic helper for stage artifacts.
func writeJSON(dir, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// digestSum is referenced by later stage funcs; declared here to keep imports local.
var _ = model.ErrStageState
