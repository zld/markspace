package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"mediapipe/internal/audio"
	"mediapipe/internal/color"
	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
	"mediapipe/internal/verify"
)

// specs returns the executor for each node, looked up in graph order.
func (e *Engine) specs() map[string]stageSpec {
	out := map[string]stageSpec{}
	for _, mt := range e.tracks {
		id := mt.Track.ID
		verifyID := id + "/verify"
		out[verifyID] = stageSpec{
			id: verifyID, trackID: id, version: versionVerify,
			params: map[string]interface{}{"frames": len(mt.Frames)},
			run:    e.runVerify,
		}
		switch mt.Track.Kind {
		case model.KindVideo:
			colorID := id + "/color"
			cutID := id + "/cut"
			out[colorID] = stageSpec{
				id: colorID, trackID: id, version: versionColor,
				params: map[string]interface{}{"target": e.cfg.ColorTarget},
				inputs: []string{verifyID},
				run:    e.runColor,
			}
			out[cutID] = stageSpec{
				id: cutID, trackID: id, version: versionCut,
				params: map[string]interface{}{"frames": len(mt.Frames)},
				inputs: []string{colorID},
				run:    e.runCut,
			}
		case model.KindAudio:
			layoutID := id + "/layout"
			resampleID := id + "/resample"
			loudID := id + "/loudness"
			out[layoutID] = stageSpec{
				id: layoutID, trackID: id, version: versionLayout,
				params:  map[string]interface{}{"channels": mt.Track.Channels},
				inputs:  []string{verifyID},
				rebuild: true,
				run:     e.runLayout,
			}
			out[resampleID] = stageSpec{
				id: resampleID, trackID: id, version: versionResample,
				params:  map[string]interface{}{"from": mt.Track.SampleRate, "to": e.cfg.TargetRate},
				inputs:  []string{layoutID},
				rebuild: true,
				run:     e.runResample,
			}
			out[loudID] = stageSpec{
				id: loudID, trackID: id, version: versionLoudness,
				params:  map[string]interface{}{"target_lufs": e.cfg.TargetLUFS, "true_peak": e.cfg.TruePeak},
				inputs:  []string{resampleID},
				rebuild: true,
				run:     e.runLoudness,
			}
		}
	}
	return out
}

func (e *Engine) mappedTrack(trackID string) *manifest.MappedTrack {
	for _, mt := range e.tracks {
		if mt.Track.ID == trackID {
			return mt
		}
	}
	return nil
}

func (e *Engine) runVerify(trackID string) (string, map[string]float64, error) {
	mt := e.mappedTrack(trackID)
	rep := verify.Track(mt, verify.FSFrameOpener(e.cfg.WorkDir))
	e.verifyReports = append(e.verifyReports, rep)
	corruptIdx, corruptOff := -1, int64(0)
	if !rep.OK {
		corruptIdx = rep.FirstError.FrameIndex
		corruptOff = rep.FirstError.Offset
		if e.corruptFrame == -1 { // first failure in schedule order wins
			e.corruptFrame = corruptIdx
			e.corruptOffset = corruptOff
		}
		return "", nil, rep.FirstError
	}
	return hashJSON(map[string]interface{}{
		"track": rep.TrackID, "frames": rep.FrameCount, "bytes": rep.BytesRead,
	}), map[string]float64{
		"frame_count":         float64(rep.FrameCount),
		"corrupt_first_frame": float64(corruptIdx),
		"corrupt_offset":      float64(corruptOff),
	}, nil
}

// VerifyReports exposes per-track verification outcomes for CLI output.
func (e *Engine) VerifyReports() []verify.Report { return e.verifyReports }

func (e *Engine) runColor(trackID string) (string, map[string]float64, error) {
	mt := e.mappedTrack(trackID)
	res, err := color.Normalize(trackID, mt.Track.Frames, e.cfg.ColorTarget)
	if err != nil {
		return "", nil, err // unknown label: classified, blocks downstream
	}
	changed := 0
	for _, fr := range res.Frames {
		if fr.Changed {
			changed++
		}
	}
	return hashJSON(map[string]interface{}{"target": e.cfg.ColorTarget, "changed": changed}),
		map[string]float64{"color_changed": float64(changed)}, nil
}

// runCut is the video conform stage downstream of color. It never runs when
// color normalization failed — the dependency-blocking guarantee exercised
// by the unknown-color test.
func (e *Engine) runCut(trackID string) (string, map[string]float64, error) {
	frames := len(e.mappedTrack(trackID).Frames)
	return hashJSON(map[string]interface{}{"stage": "cut", "frames": frames}),
		map[string]float64{"cut_frames": float64(frames)}, nil
}

func (e *Engine) runLayout(trackID string) (string, map[string]float64, error) {
	mt := e.mappedTrack(trackID)
	pcm, name, err := audio.Normalize(e.attachedPCM, mt.Track.Channels)
	if err != nil {
		return "", nil, err
	}
	e.audioOut = pcm
	return hashJSON(map[string]interface{}{"matrix": name, "samples": len(pcm)}),
		map[string]float64{"layout_samples": float64(len(pcm))}, nil
}

func (e *Engine) runResample(trackID string) (string, map[string]float64, error) {
	mt := e.mappedTrack(trackID)
	res, err := audio.ResampleStereo(e.audioOut, mt.Track.SampleRate, e.cfg.TargetRate)
	if err != nil {
		return "", nil, err
	}
	e.audioOut = res.Samples
	e.zeroPads = res.ZeroPads
	return hashJSON(map[string]interface{}{
		"rate": res.Rate, "samples": len(res.Samples), "zero_pads": res.ZeroPads,
	}), map[string]float64{
		"resampled_samples": float64(len(res.Samples)),
		"zero_pads":         float64(res.ZeroPads),
	}, nil
}

func (e *Engine) runLoudness(trackID string) (string, map[string]float64, error) {
	out, windows, err := audio.ApplyControl(e.audioOut, audio.ControlConfig{
		TargetLUFS: e.cfg.TargetLUFS, TruePeak: e.cfg.TruePeak, SampleRate: e.cfg.TargetRate,
	})
	if err != nil {
		return "", nil, err
	}
	e.audioOut = out
	e.windows = windows
	lufs := finiteLUFS(audio.IntegratedLUFS(windows))
	clips := audio.TotalClips(windows)
	return hashJSON(map[string]interface{}{
		"windows": len(windows), "clips": clips, "lufs": lufs,
	}), map[string]float64{
		"loudness_lufs": lufs,
		"clip_count":    float64(clips),
	}, nil
}

// AttachPCM supplies the locally generated audio samples. Samples are
// re-attached on every process; audio stage results are cached and skipped
// through the checkpoint.
func (e *Engine) AttachPCM(pcm []float64) { e.attachedPCM = pcm }

// hashJSON renders a stable SHA-256 over the JSON encoding of v.
func hashJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
