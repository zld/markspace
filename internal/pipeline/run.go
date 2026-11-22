package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"archive/internal/model"
	"archive/internal/parse"
	"archive/internal/render"
	"archive/internal/timeline"
)

// Config controls one pipeline run.
type Config struct {
	Out       string          // output directory containing in/
	Policy    timeline.Policy // interpretation policy (versioned)
	StopAfter string          // "", "parse" or "compose": stop after that stage
}

// Result summarises one run.
type Result struct {
	ReusedStages int
	Segments     int
	Frames       int
	Manifest     *Manifest
}

// Run executes parse → compose → render with journaled resume. A stage
// is reused only when its checkpoint matches the current input hash and
// policy version and every earlier stage was also reused; otherwise the
// pipeline recomputes from the earliest invalid stage. Render always
// re-executes so stale frames are never reused; a digest mismatch
// against a previously valid render checkpoint is an error.
func Run(cfg Config) (*Result, error) {
	inDir := filepath.Join(cfg.Out, "in")
	files, err := listInputs(inDir)
	if err != nil {
		return nil, err
	}
	if len(files) > model.MaxTracks {
		return nil, model.StageFault(model.StageParse, -1, -1, nil,
			"track count %d exceeds limit %d", len(files), model.MaxTracks)
	}
	inputHash, err := hashInputs(files)
	if err != nil {
		return nil, err
	}
	journal, err := LoadJournal(cfg.Out)
	if err != nil {
		return nil, err
	}
	res := &Result{}
	chainValid := true

	// --- stage: parse ---
	var tracks []*parse.Track
	if _, ok := journal.ValidStage("parse", inputHash, cfg.Policy.Version); ok && chainValid {
		tracks, err = readJSON[[]*parse.Track](filepath.Join(cfg.Out, "parse.json"))
		if err != nil {
			return nil, fmt.Errorf("cached parse output unreadable, recompute required: %w", err)
		}
		res.ReusedStages++
	} else {
		chainValid = false
		tracks, err = parseAll(files)
		if err != nil {
			return nil, err
		}
		digest, err := writeStageJSON(cfg.Out, "parse.json", tracks)
		if err != nil {
			return nil, err
		}
		journal.Record("parse", inputHash, cfg.Policy.Version, digest)
		if err := journal.Save(cfg.Out); err != nil {
			return nil, err
		}
	}
	if cfg.StopAfter == "parse" {
		return res, journal.Save(cfg.Out)
	}

	// --- stage: compose ---
	var segs []timeline.Segment
	if _, ok := journal.ValidStage("compose", inputHash, cfg.Policy.Version); ok && chainValid {
		segs, err = readJSON[[]timeline.Segment](filepath.Join(cfg.Out, "segments.json"))
		if err != nil {
			return nil, fmt.Errorf("cached compose output unreadable, recompute required: %w", err)
		}
		res.ReusedStages++
	} else {
		chainValid = false
		aligned := make([][]timeline.AlignedSample, len(tracks))
		for i, tr := range tracks {
			aligned[i], err = timeline.AlignTrack(i, tr)
			if err != nil {
				return nil, err
			}
		}
		segs, err = timeline.Compose(tracks, aligned, cfg.Policy)
		if err != nil {
			return nil, err
		}
		digest, err := writeStageJSON(cfg.Out, "segments.json", segs)
		if err != nil {
			return nil, err
		}
		journal.Record("compose", inputHash, cfg.Policy.Version, digest)
		if err := journal.Save(cfg.Out); err != nil {
			return nil, err
		}
	}
	if cfg.StopAfter == "compose" {
		return res, journal.Save(cfg.Out)
	}

	// --- stage: render (always re-executed) ---
	manifest, err := renderAll(cfg, inDir, tracks, segs, inputHash, journal)
	if err != nil {
		return nil, err
	}
	res.Manifest = manifest
	res.Segments = len(manifest.Segments)
	for _, s := range manifest.Segments {
		res.Frames += len(s.Frames)
	}
	return res, nil
}

func writeStageJSON(dir, name string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(dir, name), data); err != nil {
		return "", err
	}
	return render.BytesDigest(data), nil
}

func readJSON[T any](path string) (T, error) {
	var v T
	data, err := os.ReadFile(path)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	return v, nil
}

// renderAll renders every segment and builds the manifest. If a valid
// render checkpoint exists, the new manifest digest must match it:
// re-execution must be bit-identical, never a silent refresh.
func renderAll(cfg Config, inDir string, tracks []*parse.Track, segs []timeline.Segment, inputHash string, journal *Journal) (*Manifest, error) {
	framesDir := filepath.Join(cfg.Out, "frames")
	m := &Manifest{
		Version:   ManifestVersion,
		Policy:    cfg.Policy,
		InputHash: inputHash,
		StageSummaries: map[string]StageSummary{
			"parse":   {Digest: journal.Stages["parse"].Digest, Detail: fmt.Sprintf("tracks=%d", len(tracks))},
			"compose": {Digest: journal.Stages["compose"].Digest, Detail: fmt.Sprintf("segments=%d", len(segs))},
		},
	}
	totalFrames := 0
	for i := range segs {
		seg := &segs[i]
		payloads, err := readPayloads(inDir, tracks[seg.Track])
		if err != nil {
			return nil, err
		}
		paths, err := render.RenderSegment(framesDir, *seg, payloads)
		if err != nil {
			return nil, err
		}
		digest, err := render.SegmentDigest(seg.Index, paths)
		if err != nil {
			return nil, err
		}
		rec := SegmentRecord{
			Index: seg.Index, Track: seg.Track, Source: seg.Source,
			SampleCount: len(seg.Samples), SHA256: digest, Transform: seg.Transform,
		}
		for j, p := range paths {
			rel, err := filepath.Rel(cfg.Out, p)
			if err != nil {
				return nil, err
			}
			rec.Frames = append(rec.Frames, FrameRecord{
				Index: j, File: rel, SampleIndex: seg.Samples[j].SampleIndex,
				Start: seg.Samples[j].Start, Keyframe: seg.Samples[j].Keyframe,
			})
		}
		totalFrames += len(rec.Frames)
		m.Segments = append(m.Segments, rec)
	}
	// Render-stage digest covers every segment hash in order.
	h := sha256.New()
	for _, s := range m.Segments {
		h.Write([]byte(s.SHA256))
	}
	renderDigest := hex.EncodeToString(h.Sum(nil))
	m.StageSummaries["render"] = StageSummary{Digest: renderDigest, Detail: fmt.Sprintf("frames=%d", totalFrames)}
	if cp, ok := journal.ValidStage("render", inputHash, cfg.Policy.Version); ok {
		if cp.Digest != renderDigest {
			return nil, model.StageFault(model.StageRender, -1, -1, nil,
				"re-rendered output digest %s differs from checkpoint %s: refusing to reuse stale frames",
				renderDigest, cp.Digest)
		}
	}
	journal.Record("render", inputHash, cfg.Policy.Version, renderDigest)
	if err := journal.Save(cfg.Out); err != nil {
		return nil, err
	}
	if err := WriteManifest(cfg.Out, m); err != nil {
		return nil, err
	}
	return m, nil
}
