// Package pipeline wires the RTP timeline, content/container index,
// checkpoint recovery and off-screen rendering together. It decides per
// block which artifacts can be reused and which must be recomputed, and
// emits a manifest carrying quantitative, reviewable evidence.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"continuity/internal/index"
	"continuity/internal/media"
	"continuity/internal/render"
	"continuity/internal/rtp"
	"continuity/internal/wav"
)

// Config controls one offline run.
type Config struct {
	WorkDir         string
	BlockFrames     int64
	FramesPerBlock  int
	FrameWidth      int
	FrameHeight     int
	// PaceDelay, when non-zero, only throttles batch read rhythm. It is
	// applied by the caller; it never affects hashes or ordering.
	ContainerDigest string
	ContainerMeta   index.ContainerRecord
}

// Manifest is the top-level reviewable output document.
type Manifest struct {
	Version              int             `json:"version"`
	Blocks               int             `json:"blocks"`
	Duplicates           int             `json:"duplicates"`
	Reordered            int             `json:"reordered"`
	Gaps                 int             `json:"gaps"`
	LateDropped          int             `json:"late_dropped"`
	PCMHash              string          `json:"pcm_hash"`
	SegmentHash          string          `json:"segment_hash"`
	PixelMean            float64         `json:"pixel_mean"`
	PeakDBFS             float64         `json:"peak_dbfs"`
	AlignmentErrorSmp    int64           `json:"alignment_error_samples"`
	Reused               int             `json:"reused"`
	Recomputed           int             `json:"recomputed"`
	ContainerRecomputed  int             `json:"container_recomputed"`
	PCMRecomputed        int             `json:"pcm_recomputed"`
	RollbackBlocks       int             `json:"rollback_blocks,omitempty"`
	Segments             []render.Segment `json:"segments"`
	Container            *index.ContainerRecord `json:"container,omitempty"`
}

// Runner executes one or more runs against a persistent work directory.
type Runner struct {
	cfg   Config
	store *index.Store
}

// New opens (or creates) the index inside cfg.WorkDir.
func New(cfg Config) (*Runner, error) {
	store, err := index.OpenStore(filepath.Join(cfg.WorkDir, "index"))
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, store: store}, nil
}

// Store exposes the index for callers that need rollback details.
func (r *Runner) Store() *index.Store { return r.store }

// BlockInput is a reconstructed block plus the transport evidence that
// belongs to it.
type BlockInput struct {
	Block            rtp.Block
	Layout           wav.Layout
	AlignmentSamples int64
}

// Run processes a fully reconstructed, ordered set of blocks.
func (r *Runner) Run(blocks []BlockInput, stats rtp.Stats) (Manifest, error) {
	layout := firstLayout(blocks)
	segments := make([]render.Segment, 0, len(blocks))
	reused, recomputed := 0, 0
	var pixelAccum float64
	var pixelWeight int64
	var peak float64

	// Re-validate the stored prefix against freshly computed digests; the
	// first mismatch invalidates that block and every dependent one.
	if err := r.invalidateChanged(blocks, layout); err != nil {
		return Manifest{}, err
	}

	for i, bi := range blocks {
		digest := index.ContentDigest(index.ContentInput{
			PCM:         bi.Block.PCM,
			Layout:      layout,
			StartSample: bi.Block.StartSample,
			EndSample:   bi.Block.EndSample,
		})
		loud := index.PeakLoudness(bi.Block.PCM)
		if loud.PeakDBFS > peak {
			peak = loud.PeakDBFS
		}

		if stored, ok := r.store.FindHash(i); ok && stored == digest {
			// Reuse: block content is unchanged and the checkpoint is
			// valid. The deterministic segment is regenerated but no
			// checkpoint commit happens.
			seg, err := r.renderBlock(i, bi, digest, loud.PeakDBFS)
			if err != nil {
				return Manifest{}, err
			}
			reused++
			segments = append(segments, seg)
			r.accumulatePixel(seg, &pixelAccum, &pixelWeight)
			continue
		}

		// Recompute: render, then atomically commit the checkpoint.
		seg, err := r.renderBlock(i, bi, digest, loud.PeakDBFS)
		if err != nil {
			return Manifest{}, err
		}
		rec := index.BlockRecord{
			Index:       i,
			StartSample: bi.Block.StartSample,
			EndSample:   bi.Block.EndSample,
			ContentHash: digest,
			GapSamples:  bi.Block.GapSamples,
			PeakDBFS:    loud.PeakDBFS,
			Complete:    true,
		}
		for _, g := range bi.Block.GapRanges {
			rec.GapRanges = append(rec.GapRanges, indexMediaGap(g))
		}
		if err := r.store.CommitBlock(rec); err != nil {
			return Manifest{}, err
		}
		recomputed++
		segments = append(segments, seg)
		r.accumulatePixel(seg, &pixelAccum, &pixelWeight)
	}

	containerRecomputed := r.syncContainer()
	m := Manifest{
		Version:             1,
		Blocks:              len(blocks),
		Duplicates:          stats.Duplicates,
		Reordered:           stats.Reordered,
		Gaps:                stats.Gaps,
		LateDropped:         stats.LateDropped,
		PixelMean:           round2(weightedMean(pixelAccum, pixelWeight)),
		PeakDBFS:            round2(peak),
		AlignmentErrorSmp:   maxAlignment(blocks),
		Reused:              reused,
		Recomputed:          recomputed,
		ContainerRecomputed: containerRecomputed,
		PCMRecomputed:       recomputed,
		Segments:            segments,
	}
	if cr, ok := r.store.Container(); ok {
		c := cr
		m.Container = &c
	}
	m.PCMHash = combinePCMHash(segments)
	m.SegmentHash = combineSegmentHash(segments)
	if rb, ok := r.store.Rollback(); ok {
		m.RollbackBlocks = rb.Dropped
	}
	if err := r.writeManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// invalidateChanged truncates stored blocks from the first position whose
// freshly computed digest differs from the checkpoint.
func (r *Runner) invalidateChanged(blocks []BlockInput, layout wav.Layout) error {
	for i, bi := range blocks {
		digest := index.ContentDigest(index.ContentInput{
			PCM:         bi.Block.PCM,
			Layout:      layout,
			StartSample: bi.Block.StartSample,
			EndSample:   bi.Block.EndSample,
		})
		stored, ok := r.store.FindHash(i)
		if !ok {
			break
		}
		if stored != digest {
			if _, err := r.store.TruncateFrom(i); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// renderBlock produces a fresh off-screen segment and writes it.
func (r *Runner) renderBlock(i int, bi BlockInput, digest string, peakDBFS float64) (render.Segment, error) {
	in := render.Input{
		BlockIndex:       i,
		StartSample:      bi.Block.StartSample,
		EndSample:        bi.Block.EndSample,
		PCM:              bi.Block.PCM,
		InputDigest:      digest,
		PeakDBFS:         peakDBFS,
		AlignmentSamples: bi.AlignmentSamples,
		GapSamples:       bi.Block.GapSamples,
		FramesPerBlock:   r.cfg.FramesPerBlock,
		FrameWidth:       r.cfg.FrameWidth,
		FrameHeight:      r.cfg.FrameHeight,
	}
	for _, g := range bi.Block.GapRanges {
		in.GapRanges = append(in.GapRanges, render.GapRange{StartSample: g.StartSample, EndSample: g.EndSample})
	}
	seg, err := render.Render(in)
	if err != nil {
		return render.Segment{}, err
	}
	segDir := filepath.Join(r.cfg.WorkDir, "segments")
	if _, err := render.WriteSegment(segDir, seg); err != nil {
		return render.Segment{}, err
	}
	return seg, nil
}

func (r *Runner) accumulatePixel(seg render.Segment, accum *float64, weight *int64) {
	framePixels := int64(r.cfg.FrameWidth) * int64(r.cfg.FrameHeight)
	if framePixels == 0 {
		framePixels = 16 * 16
	}
	w := framePixels * int64(seg.Frames)
	*accum += seg.PixelMean * float64(w)
	*weight += w
}

// syncContainer updates only the container index. It returns 1 when the
// container record changed (or was first written), 0 when identical. PCM
// blocks are never touched here.
func (r *Runner) syncContainer() int {
	if r.cfg.ContainerDigest == "" {
		return 0
	}
	if existing, ok := r.store.Container(); ok && existing.Hash == r.cfg.ContainerMeta.Hash {
		return 0
	}
	_ = r.store.SetContainer(r.cfg.ContainerMeta)
	return 1
}

func (r *Runner) writeManifest(m Manifest) error {
	dir := filepath.Join(r.cfg.WorkDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return media.WrapError(media.CatIO, "pipeline: manifest dir", err)
	}
	final := filepath.Join(dir, "manifest.json")
	tmp := final + ".tmp"
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return media.WrapError(media.CatIO, "pipeline: manifest marshal", err)
	}
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return media.WrapError(media.CatIO, "pipeline: manifest temp", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return media.WrapError(media.CatIO, "pipeline: manifest rename", err)
	}
	return nil
}

func firstLayout(blocks []BlockInput) wav.Layout {
	if len(blocks) == 0 {
		l, _ := wav.NewLayout(2, 16, 48000)
		return l
	}
	return blocks[0].Layout
}

func maxAlignment(blocks []BlockInput) int64 {
	var m int64
	for _, b := range blocks {
		if b.AlignmentSamples > m {
			m = b.AlignmentSamples
		}
	}
	return m
}

func weightedMean(accum float64, weight int64) float64 {
	if weight == 0 {
		return 0
	}
	return accum / float64(weight)
}

func round2(v float64) float64 {
	bits := float64(100)
	return float64(int64(v*bits+0.5)) / bits
}

func indexMediaGap(g rtp.GapRange) index.BlockRecordGap {
	return index.BlockRecordGap{StartSample: g.StartSample, EndSample: g.EndSample}
}

// combinePCMHash folds every block's input digest in order into one
// stream-level PCM digest.
func combinePCMHash(segments []render.Segment) string {
	h := sha256.New()
	h.Write([]byte("continuity-pcm-stream-v1\n"))
	for _, s := range segments {
		h.Write([]byte(s.InputDigest))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// combineSegmentHash folds every segment hash in order.
func combineSegmentHash(segments []render.Segment) string {
	h := sha256.New()
	h.Write([]byte("continuity-segment-stream-v1\n"))
	for _, s := range segments {
		h.Write([]byte(s.SegmentHash))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
