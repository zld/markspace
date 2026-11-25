package pipeline

import (
	"encoding/json"

	"mediasync/internal/fixture"
	"mediasync/internal/metadata"
	"mediasync/internal/model"
	"mediasync/internal/render"
	"mediasync/internal/report"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
)

// runMetadata parses both containers, adjudicates with fixed priority and
// emits the verdict as the stage's single JSON artifact.
func (p *Pipeline) runMetadata(mxfData, mp4Data []byte, rec *model.Recorder) ([]artifact, error) {
	mxf, err := metadata.ParseMXF(mxfData, rec)
	if err != nil {
		return nil, err
	}
	mp4, err := metadata.ParseMP4(mp4Data, rec)
	if err != nil {
		return nil, err
	}
	verdict := metadata.Adjudicate(mxf, mp4, metadata.DefaultDefaults(), rec)
	if rec.HasError() {
		return nil, errFromRecorder(rec)
	}
	data, err := json.MarshalIndent(verdict, "", "  ")
	if err != nil {
		return nil, err
	}
	return []artifact{{name: "verdict.json", data: data}}, nil
}

// runRecovery drives the RTP engine over the captured datagrams and
// bandwidth trace, emitting ordered packets plus the recovery summary.
func (p *Pipeline) runRecovery(fx *fixture.Fixture, rec *model.Recorder) (
	[]rtp.Packet, rtp.RecoveryStats, []artifact) {
	cfg := rtp.RecoveryConfig{Reorder: p.cfg.ReorderCfg, Jitter: p.cfg.JitterCfg}
	ordered, stats := rtp.RecoverStream(fx.RTPFrames, fx.Bandwidth, cfg, rec)
	out, _ := json.MarshalIndent(stats, "", "  ")
	return ordered, stats, []artifact{{name: "recovery.json", data: out}}
}

// runAlign builds deterministic cues from the fixture's known track layout
// and projects them onto the master timeline.
func (p *Pipeline) runAlign(fx *fixture.Fixture, rec *model.Recorder) (
	*sync.Result, []artifact) {
	var cues []sync.Cue
	for i := 0; i < fx.VideoFrames; i++ {
		cues = append(cues, sync.Cue{Track: "video", Index: i, Rate: 30,
			Start: int64(i), Duration: 1})
	}
	for i := 0; i < fx.AudioFragments; i++ {
		cues = append(cues, sync.Cue{Track: "audio", Index: i, Rate: 48000,
			Start: int64(i) * 48000, Duration: 48000})
	}
	for i := 0; i < fx.SubtitleCues; i++ {
		cues = append(cues, sync.Cue{Track: "subtitle", Index: i, Rate: 1000,
			Start: int64(i) * 500, Duration: 400})
	}
	result, err := sync.Align(cues, p.cfg.AlignmentCfg, rec)
	if err != nil {
		return result, nil
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return result, []artifact{{name: "alignment.json", data: data}}
}

// runRender produces deterministic frames and audio fragments and emits one
// JSON artifact per media kind plus the binary PCM for audio integrity.
func (p *Pipeline) runRender(aligned *sync.Result, rec *model.Recorder) (
	[]render.Frame, []render.AudioFragment, []artifact) {
	r := render.NewRenderer(p.cfg.Render, uint64(p.cfg.Seed))
	frames, audio := r.RenderAll(aligned.Cues)
	var arts []artifact
	frameJSON, _ := json.MarshalIndent(frames, "", "  ")
	arts = append(arts, artifact{name: "frames.json", data: frameJSON})
	audioJSON, _ := json.MarshalIndent(audio, "", "  ")
	arts = append(arts, artifact{name: "audio.json", data: audioJSON})
	// Concatenate PCM into one deterministic blob for manifest hashing.
	var pcm []byte
	for _, a := range audio {
		pcm = append(pcm, a.PCM...)
	}
	arts = append(arts, artifact{name: "audio.pcm", data: pcm})
	return frames, audio, arts
}

// runReport serializes the aggregate quantitative summary.
func (p *Pipeline) runReport(summary *report.Summary) ([]artifact, error) {
	data, err := summary.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return []artifact{{name: "summary.json", data: data}}, nil
}

func errFromRecorder(rec *model.Recorder) error {
	return &stageError{msg: rec.JoinMessages()}
}

type stageError struct{ msg string }

func (e *stageError) Error() string { return "pipeline: " + e.msg }
