package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"mediad/internal/ingest"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/plan"
	"mediad/internal/quality"
	"mediad/internal/stage"
	"mediad/internal/timeline"
)

func itoa(v int) string { return strconv.Itoa(v) }
func ftoa(v float64) string {
	return strconv.FormatFloat(v, 'f', 4, 64)
}

// storedPacket is the JSON representation persisted between ingest and later
// stages; it carries everything needed to rebuild timelines after a restart.
type storedPacket struct {
	ExtSequence uint64 `json:"ext_sequence"`
	Sequence    uint16 `json:"sequence"`
	RTPTime     uint32 `json:"rtp_time"`
	Bytes       int    `json:"bytes"`
	DurationNS  int64  `json:"duration_ns"`
	DurationTC  int64  `json:"duration_tc"`
}

type storedCapture struct {
	Spec             ingest.TrackSpec `json:"spec"`
	Packets          []storedPacket   `json:"packets"`
	Gaps             []timeline.Gap   `json:"gaps"`
	Duplicates       uint64           `json:"duplicates"`
	Accepted         uint64           `json:"accepted"`
	ObjectiveQuality float64          `json:"objective_quality"`
}

func writeJSON(dir, rel string, v interface{}) error {
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rel), raw, 0o644)
}

func readJSON(dir, rel string, v interface{}) error {
	raw, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		return model.ErrStateCorrupt.At("", 0, "pipeline", 0,
			"missing artifact "+rel+": "+err.Error())
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return model.ErrStateCorrupt.At("", 0, "pipeline", 0, err.Error())
	}
	return nil
}

func savePackets(dir, rel string, spec ingest.TrackSpec,
	packets []*ingest.AudioPacket, r *timeline.Reorderer) error {
	expected := r.Accepted
	for _, g := range r.Gaps {
		expected += g.ToSeq - g.FromSeq + 1
	}
	st := storedCapture{
		Spec: spec, Gaps: r.Gaps, Duplicates: r.Duplicates, Accepted: r.Accepted,
		ObjectiveQuality: quality.ObjectiveScore(r.Accepted, expected, r.Gaps),
	}
	for _, pkt := range packets {
		st.Packets = append(st.Packets, storedPacket{
			ExtSequence: pkt.ExtSequence, Sequence: pkt.Sequence, RTPTime: pkt.RTPTime,
			Bytes: len(pkt.Payload), DurationNS: int64(pkt.DurationNS), DurationTC: pkt.DurationTC,
		})
	}
	return writeJSON(dir, rel, st)
}

func loadPackets(dir, rel string) (ingest.TrackSpec, []*ingest.AudioPacket,
	[]timeline.Gap, uint64, uint64, float64, error) {
	var st storedCapture
	if err := readJSON(dir, rel, &st); err != nil {
		return ingest.TrackSpec{}, nil, nil, 0, 0, 0, err
	}
	packets := make([]*ingest.AudioPacket, 0, len(st.Packets))
	for _, sp := range st.Packets {
		packets = append(packets, &ingest.AudioPacket{
			Track: st.Spec.ID, ExtSequence: sp.ExtSequence, Sequence: sp.Sequence,
			RTPTime: sp.RTPTime, Payload: make([]byte, sp.Bytes),
			DurationNS: model.Nano(sp.DurationNS), DurationTC: sp.DurationTC,
		})
	}
	gaps := st.Gaps
	if gaps == nil {
		gaps = []timeline.Gap{}
	}
	return st.Spec, packets, gaps, st.Duplicates, st.Accepted, st.ObjectiveQuality, nil
}

func saveTimeline(dir, rel string, tl *timeline.Timeline) error {
	return writeJSON(dir, rel, tl)
}

func loadTimeline(dir, rel string) (*timeline.Timeline, error) {
	var tl timeline.Timeline
	if err := readJSON(dir, rel, &tl); err != nil {
		return nil, err
	}
	return &tl, nil
}

func saveDecision(dir, rel string, d *plan.Decision) error {
	return writeJSON(dir, rel, d)
}

func loadDecision(dir, rel string) (*plan.Decision, error) {
	var d plan.Decision
	if err := readJSON(dir, rel, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// estimateBytes derives the byte count that hits the target bitrate for the
// timeline duration: bytes = bitrate * duration / (8 * 1e9).
func estimateBytes(tl *timeline.Timeline, targetBPS uint64) int64 {
	if tl == nil || tl.TotalDurNS <= 0 {
		return 0
	}
	scaled, err := model.Scale(int64(targetBPS), int64(tl.TotalDurNS), int64(model.Second)*8)
	if err != nil {
		return 0
	}
	return scaled
}

// writeDeliverables emits the deterministic audio artifact (gap holes omitted,
// never filled) and the self-hashing manifest.
func (p *Pipeline) writeDeliverables(ctx *stage.Context, tr *trackRun) ([]string, error) {
	audioRel := filepath.Join("deliver", tr.spec.ID+".audio")
	audioPath := filepath.Join(ctx.Dir, audioRel)
	if err := os.MkdirAll(filepath.Dir(audioPath), 0o755); err != nil {
		return nil, err
	}
	var content []byte
	for _, f := range tr.timeline.Frames {
		content = append(content, []byte(strconv.FormatUint(f.ExtSequence, 10))...)
		content = append(content, ':')
		content = append(content, []byte(strconv.FormatInt(int64(f.DurationNS), 10))...)
		content = append(content, '\n')
	}
	if err := os.WriteFile(audioPath, content, 0o644); err != nil {
		return nil, err
	}
	audioSHA, err := stage.SHA256File(audioPath)
	if err != nil {
		return nil, err
	}
	var payloadBytes int64
	for _, f := range tr.timeline.Frames {
		payloadBytes += int64(f.Bytes)
	}
	expected := tr.accepted
	for _, g := range tr.gaps {
		expected += g.ToSeq - g.FromSeq + 1
	}
	rep, err := quality.Measure(p.cfg.TargetBitrate, payloadBytes, tr.timeline.TotalDurNS,
		tr.accepted, expected, tr.gaps, []*timeline.Timeline{tr.timeline})
	if err != nil {
		return nil, err
	}
	b := manifest.NewBuilder()
	b.AddTrack(tr.timeline, tr.action, map[string]string{audioRel: audioSHA})
	stageDigests := map[string]string{}
	for _, name := range p.runner.State().Order {
		st := p.runner.State().Stages[name]
		if st.Status == stage.StatusCompleted {
			for rel, sum := range st.OutputSHA {
				stageDigests[rel] = sum
			}
		}
	}
	b.SetQuality(*rep, stageDigests)
	if _, err := b.Write(ctx.Dir); err != nil {
		return nil, err
	}
	return []string{audioRel, manifest.FileName}, nil
}
