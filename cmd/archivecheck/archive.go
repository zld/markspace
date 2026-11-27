package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"archivecheck/internal/manifest"
	"archivecheck/internal/pipeline"
)

// assemble runs one complete build into dir.
func assemble(dir string, samples []*sample, res *analysis, cfg pipeline.Config) (*manifest.Manifest, error) {
	b := manifest.NewBuilder(dir)
	for _, s := range samples {
		if err := b.AddSource(s.source); err != nil {
			return nil, err
		}
	}
	for _, t := range res.tracks {
		b.AddTrack(t)
	}
	for _, d := range res.damage {
		b.AddDamage(d)
	}
	for _, r := range res.ranges {
		b.AddTimeRange(r)
	}

	// Layer 1: one normalised artefact per source.
	var layer1 []string
	for _, s := range samples {
		kind, payload := layerOne(s, res)
		deriv, err := b.AddDerivative(manifest.DerivativeSpec{
			Kind: kind, Layer: 1, Data: payload, Deps: []string{s.source.Digest},
		})
		if err != nil {
			return nil, err
		}
		layer1 = append(layer1, deriv.Digest)
	}
	// Layer 2: one archive bundle over every layer-1 artefact.
	bundle := map[string]any{
		"chunkSize": cfg.ChunkSize,
		"queueSize": cfg.QueueSize,
		"tracks":    len(res.tracks),
		"damage":    len(res.damage),
		"ranges":    len(res.ranges),
		"codes":     len(res.codes),
		"rejected":  len(res.bad),
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if _, err := b.AddDerivative(manifest.DerivativeSpec{Kind: "archive-bundle", Layer: 2, Data: payload, Deps: layer1}); err != nil {
		return nil, err
	}
	return b.Build()
}

// layerOne renders the normalised artefact for a source.
func layerOne(s *sample, res *analysis) (string, []byte) {
	name := filepath.Base(s.source.Name)
	switch {
	case strings.HasSuffix(name, ".mp4"):
		st := res.mp4[name]
		return "stream-structure", mustJSON(map[string]any{
			"source": s.source.Name, "boxes": st.BoxCount, "brand": st.Brand,
			"tracks": st.Tracks, "damage": st.Damaged,
		})
	case strings.HasSuffix(name, ".rtp"):
		return "rtp-analysis", mustJSON(map[string]any{
			"source": s.source.Name, "packets": res.rtp.Packets, "lost": res.rtp.Lost,
			"gaps": res.rtp.Gaps, "durationNanos": int64(res.rtp.Duration),
		})
	case strings.HasSuffix(name, ".srt"):
		return "subtitle-track", mustJSON(res.srt)
	case strings.HasSuffix(name, ".vtt"):
		return "subtitle-track", mustJSON(res.vtt)
	case strings.HasSuffix(name, ".txt"):
		return "timecode-table", mustJSON(map[string]any{
			"source": s.source.Name, "valid": res.codes, "rejected": len(res.bad),
		})
	}
	return "source-scan", mustJSON(map[string]any{"source": s.source.Name, "bytes": s.source.Size})
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("archivecheck: marshal derivative: %v", err))
	}
	return data
}

// buildArchive writes the derivative store and the manifest, then rebuilds
// from the same sources to report reuse. It returns the manifest and the
// counts of the repeat run.
func buildArchive(out io.Writer, dir string, samples []*sample, res *analysis, cfg pipeline.Config) (*manifest.Manifest, manifest.Counts, error) {
	first, err := assemble(dir, samples, res, cfg)
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	sum, err := first.Checksum()
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	fmt.Fprintln(out, "== manifest ==============================================")
	fmt.Fprintf(out, "  %s\n", first.Summary())
	fmt.Fprintf(out, "  counts: added=%d reused=%d changed=%d invalidated=%d\n",
		first.Counts.Added, first.Counts.Reused, first.Counts.Changed, first.Counts.Invalidated)
	fmt.Fprintf(out, "  checksum: %s\n", sum)
	for _, d := range first.Derivatives {
		fmt.Fprintf(out, "  derivative layer=%d kind=%-18s digest=%s deps=%d\n", d.Layer, d.Kind, short(d.Digest), len(d.Deps))
	}

	repeatManifest, err := assemble(dir, samples, res, cfg)
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	fmt.Fprintf(out, "  repeat run: added=%d reused=%d changed=%d invalidated=%d\n",
		repeatManifest.Counts.Added, repeatManifest.Counts.Reused, repeatManifest.Counts.Changed, repeatManifest.Counts.Invalidated)
	repeatSum, err := repeatManifest.Checksum()
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	fmt.Fprintf(out, "  repeat checksum: %s\n\n", repeatSum)
	if repeatSum != sum {
		return nil, manifest.Counts{}, fmt.Errorf("manifest checksum changed between identical runs")
	}
	// The repeat run rewrote manifest.json with reuse counts; verify that copy.
	return repeatManifest, repeatManifest.Counts, nil
}
