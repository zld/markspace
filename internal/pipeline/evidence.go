package pipeline

import (
	"math"
	"os"
	"path/filepath"
	"strings"

	"mediatimeline/asset"
	"mediatimeline/evidence"
)

// renderEvidence re-opens the selected fragment of every merged video
// interval, renders its frames offscreen and aggregates audio statistics.
// Damaged intervals are skipped without stopping the remaining intervals.
func renderEvidence(cfg Config, merged asset.MergeResult, res *Result, fail func(Failure)) {
	var hashes []string
	var sumR, sumG, sumB, pixelN float64
	validFrames := 0
	var peakSum float64
	var peakCount int
	anyAudio := false

	evidenceDir := filepath.Join(cfg.OutDir, "evidence")
	_ = os.MkdirAll(evidenceDir, 0o755)

	for _, mi := range merged.Intervals {
		if !strings.HasPrefix(mi.Selected.TrackID, "video") {
			// Audio interval: parse PCM and fold into the level statistics.
			stats, ok := analyzeIntervalAudio(cfg.OutDir, mi.Selected)
			if !ok {
				continue
			}
			anyAudio = true
			if !math.IsInf(stats.PeakDBFS, -1) {
				peakSum += stats.PeakDBFS
				peakCount++
			}
			continue
		}
		frag, ok := parseSelected(cfg.OutDir, mi.Selected, fail)
		if !ok {
			continue
		}
		mdat := readMDAT(sourceToPath(cfg.OutDir, mi.Selected.Source))
		payloads := splitMDATPayloads(frag, mdat)
		for i, payload := range payloads {
			fe, err := evidence.RenderFrame(i, payload)
			if err != nil {
				fail(Failure{Code: "EVIDENCE_FRAME_BAD", Stage: StageEvidence,
					Asset: mi.Selected.Source, Message: err.Error()})
				continue
			}
			hashes = append(hashes, fe.SHA256)
			sumR += fe.PixelMeanR
			sumG += fe.PixelMeanG
			sumB += fe.PixelMeanB
			pixelN++
			validFrames++
			// Persist the PPM next to the summary for offline inspection.
			frame, _ := evidence.DecodeFrame(payload)
			ppm := encodePPMPublic(frame)
			name := "frame_" + mi.Selected.Digest[:8] + "_" + itoa(i) + ".ppm"
			_ = os.WriteFile(filepath.Join(evidenceDir, name), ppm, 0o644)
		}
	}
	stableFrameHashSort(hashes)
	res.FrameHashes = hashes
	res.ValidFrames = validFrames
	if pixelN > 0 {
		res.PixelMeanRGB = [3]float64{sumR / pixelN, sumG / pixelN, sumB / pixelN}
	}
	if anyAudio && peakCount == 0 {
		res.AudioSilence = true
	} else if peakCount > 0 {
		res.AudioPeakDBFS = peakSum / float64(peakCount)
	}
}

func parseSelected(outDir string, v asset.Version, fail func(Failure)) (*asset.Fragment, bool) {
	path := sourceToPath(outDir, v.Source)
	ts := uint64(90000)
	if strings.Contains(v.Source, "audio") {
		ts = 48000
	}
	f, err := os.Open(path)
	if err != nil {
		fail(Failure{Code: "EVIDENCE_OPEN_FAILED", Stage: StageEvidence, Asset: v.Source, Message: err.Error()})
		return nil, false
	}
	defer f.Close()
	frag, err := asset.ParseFragment(f, ts)
	if err != nil {
		fail(Failure{Code: "EVIDENCE_PARSE_FAILED", Stage: StageEvidence,
			Asset: v.Source, Message: err.Error()})
		return nil, false
	}
	return frag, true
}

func analyzeIntervalAudio(outDir string, v asset.Version) (evidence.AudioStats, bool) {
	path := sourceToPath(outDir, v.Source)
	f, err := os.Open(path)
	if err != nil {
		return evidence.AudioStats{}, false
	}
	defer f.Close()
	frag, err := asset.ParseFragment(f, 48000)
	if err != nil {
		return evidence.AudioStats{}, false
	}
	pcm := readMDAT(path)
	stats, err := evidence.AnalyzeAudio(pcm)
	if err != nil {
		return evidence.AudioStats{}, false
	}
	_ = frag
	return stats, true
}

// sourceToPath maps a registry source ("hls:hls/demo/v1/seg0.m4s") to disk.
func sourceToPath(outDir, source string) string {
	parts := strings.SplitN(source, ":", 2)
	rel := parts[len(parts)-1]
	return filepath.Join(outDir, rel)
}
