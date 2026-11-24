package engine

import (
	"sort"
	"time"

	"mediapipe/internal/audio"
	"mediapipe/internal/model"
)

// rehydrateCorruption restores the aggregate first-corruption location from a
// previous process's verify metrics so resumed summaries stay identical.
func (e *Engine) rehydrateCorruption() {
	ids := make([]string, 0)
	for id := range e.metrics {
		if len(id) > 7 && id[len(id)-7:] == "/verify" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		m := e.metrics[id]
		if cf, ok := m["corrupt_first_frame"]; ok && int(cf) != -1 {
			e.corruptFrame = int(cf)
			e.corruptOffset = int64(m["corrupt_offset"])
			return
		}
	}
}

// metric reads a persisted stage metric, falling back to 0. Metrics live in
// the checkpoint so a resumed run reconstructs its summary without
// re-executing any stage.
func (e *Engine) metric(stage, key string) float64 {
	if m, ok := e.metrics[stage]; ok {
		return m[key]
	}
	return 0
}

func (e *Engine) audioStageID(suffix string) string {
	for _, mt := range e.tracks {
		if mt.Track.Kind == model.KindAudio {
			return mt.Track.ID + suffix
		}
	}
	return ""
}

// CurrentSummary builds the summary from the engine's current state without
// running any stage. It lets a CLI report an interrupted partial run.
func (e *Engine) CurrentSummary() *Summary {
	s, _ := e.summarize(time.Now(), nil)
	return s
}

func (e *Engine) summarize(startedAt time.Time, firstErr error) (*Summary, error) {
	// frame_count reports delivered video frames; audio chunk counts are
	// expressed through the audio stage metrics instead.
	frameCount := 0
	for _, mt := range e.tracks {
		if mt.Track.Kind == model.KindVideo {
			frameCount += len(mt.Frames)
		}
	}
	blocked := e.BlockedStagesSnapshot()
	sort.Strings(blocked)

	loudID := e.audioStageID("/loudness")
	resampleID := e.audioStageID("/resample")
	// Prefer live values when stages ran in this process; otherwise the
	// persisted metrics reproduce identical numbers after a resume.
	lufs := finiteLUFS(audio.IntegratedLUFS(e.windows))
	if len(e.windows) == 0 && loudID != "" {
		lufs = e.metric(loudID, "loudness_lufs")
	}
	clips := audio.TotalClips(e.windows)
	if len(e.windows) == 0 && loudID != "" {
		clips = int(e.metric(loudID, "clip_count"))
	}
	zeroPads := e.zeroPads
	if zeroPads == 0 && resampleID != "" {
		zeroPads = int(e.metric(resampleID, "zero_pads"))
	}
	s := &Summary{
		TrackCount:        len(e.tracks),
		FrameCount:        frameCount,
		CorruptFirstFrame: e.corruptFrame,
		CorruptOffset:     e.corruptOffset,
		ResumeMS:          time.Since(startedAt).Milliseconds(),
		LoudnessLUFS:      lufs,
		ClipCount:         clips,
		ReusedStages:      e.reusedCount,
		ZeroPads:          zeroPads,
		BlockedStages:     blocked,
		OutputSHA256:      hashJSON(e.deterministicOutput()),
	}
	return s, firstErr
}

// deterministicOutput hashes exactly the values repeated runs must agree on.
// It deliberately excludes transient PCM buffers and timestamps: only stage
// digests plus persisted metrics enter the hash, so an interrupted-then-
// resumed run and a single uninterrupted run produce the same digest.
func (e *Engine) deterministicOutput() map[string]interface{} {
	blocked := e.BlockedStagesSnapshot()
	sort.Strings(blocked)
	return map[string]interface{}{
		"digests":       e.digests,
		"metrics":       e.metrics,
		"failed":        sortedKeys(e.failed),
		"blocked":       blocked,
		"corrupt_frame": e.corruptFrame,
		"corrupt_off":   e.corruptOffset,
	}
}

// BlockedStagesSnapshot returns the currently blocked node ids unsorted.
func (e *Engine) BlockedStagesSnapshot() []string {
	out := make([]string, 0, len(e.blocked))
	for id := range e.blocked {
		out = append(out, id)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
