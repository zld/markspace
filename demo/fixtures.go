package demo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pion/rtp"

	"mediapack/manifest"
	"mediapack/media"
	"mediapack/merr"
	"mediapack/pipeline"
	"mediapack/render"
	"mediapack/timeline"
)

type fixtureResult struct {
	name      string
	detected  bool
	recovered bool
	detail    string
}

// VerifyFixtures generates the boundary fixtures and checks that each
// failure class is identified and handled. Exit-code mapping happens in
// main: any failed fixture makes the command exit 3.
func VerifyFixtures(out string) error {
	if out == "" {
		dir, err := os.MkdirTemp("", "mediapack-fixtures")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		out = dir
	} else if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	results := []fixtureResult{
		fixtureCycle(),
		fixtureTruncatedMP4(out),
		fixtureRTPDupReorder(),
		fixtureRTPSeqWrap(),
		fixtureCorruptCheckpoint(out),
	}
	failed := 0
	for _, r := range results {
		fmt.Printf("fixture=%s detected=%t recovered=%t detail=%q\n", r.name, r.detected, r.recovered, r.detail)
		if !r.detected || !r.recovered {
			failed++
		}
	}
	if failed > 0 {
		return merr.Corruptf("verify-fixtures", "", "%d of %d fixtures failed", failed, len(results))
	}
	return nil
}

// fixtureCycle: a cyclic track graph must be rejected with stage+track context.
func fixtureCycle() fixtureResult {
	g := timeline.NewGraph()
	_ = g.Add(&timeline.Track{ID: "a", DependsOn: []string{"b"}})
	_ = g.Add(&timeline.Track{ID: "b", DependsOn: []string{"a"}})
	_, err := g.Order()
	ok := err != nil && merr.KindOf(err) == merr.KindCorrupt &&
		strings.Contains(err.Error(), "stage=graph") && strings.Contains(err.Error(), "a")
	return fixtureResult{"cycle-dependency", ok, ok, fmt.Sprint(err)}
}

// fixtureTruncatedMP4: a truncated sample stops the track at the
// corruption offset; the manifest must export the missing frames as
// holes, never as valid frames.
func fixtureTruncatedMP4(out string) fixtureResult {
	dir := filepath.Join(out, "truncated-mp4")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fixtureResult{"truncated-mp4", false, false, err.Error()}
	}
	p := filepath.Join(dir, "trunc.mp4")
	sizes := make([]uint32, 60)
	for i := range sizes {
		sizes[i] = 500
	}
	if err := media.WriteSyntheticMP4(p, []media.SynthTrack{
		{TrackID: 1, MediaType: "vide", Timescale: 90000, Delta: 3000, Sizes: sizes, Fill: fillFunc(99)},
	}); err != nil {
		return fixtureResult{"truncated-mp4", false, false, err.Error()}
	}
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, b[:len(b)*3/5], 0o644); err != nil {
		return fixtureResult{"truncated-mp4", false, false, err.Error()}
	}
	f, err := media.ReadFile(p)
	if err != nil || len(f.Tracks) == 0 {
		return fixtureResult{"truncated-mp4", false, false, "demux failed outright"}
	}
	tr := f.Tracks[0]
	detected := tr.CorruptOffset >= 0 && len(tr.Samples) < 60
	// composite the damaged track: missing frames become explicit holes
	src := render.TrackSource{TrackID: "v", Video: true, Required: true}
	for _, s := range tr.Samples {
		src.Frames = append(src.Frames, render.SourceFrame{
			Time: timeline.TicksToNanos(s.DTS, 90000), Data: s.Data, Size: int(s.Size)})
	}
	comp := &render.Compositor{Width: 64, Height: 64, FPS: 30, Seed: 1}
	end := timeline.FrameTime(0, 30, 60)
	res, rerr := comp.Render([]render.TrackSource{src}, nil, 0, end)
	if rerr != nil {
		return fixtureResult{"truncated-mp4", detected, false, rerr.Error()}
	}
	m := &manifest.Manifest{
		Version: 1, Timebase: "ns", EndNanos: end, FPS: 30,
		FrameIntervalNanos: timeline.FrameInterval(30),
		Stats:              manifest.StatsBlock{Frames: res.Stats.Frames, ValidFrames: res.Stats.ValidFrames, GapFrames: res.Stats.GapFrames},
		Tracks:             []manifest.TrackEntry{{ID: "v", Kind: "video", CorruptOffset: tr.CorruptOffset}},
	}
	for i, fr := range res.Frames {
		m.Frames = append(m.Frames, manifest.FrameEntry{
			Index: i, TimeNanos: fr.Time, Valid: fr.Valid, Hash: render.HashHex(fr.Hash), Size: fr.Size})
	}
	verr := manifest.Verify(m)
	wantGaps := 60 - len(tr.Samples)
	recovered := res.Stats.GapFrames == wantGaps && verr == nil && m.Tracks[0].CorruptOffset >= 0
	detail := fmt.Sprintf("corrupt_at=%d samples=%d/60 gap_frames=%d verify=%v", tr.CorruptOffset, len(tr.Samples), res.Stats.GapFrames, verr)
	return fixtureResult{"truncated-mp4", detected, recovered, detail}
}

// fixtureRTPDupReorder: duplicates must not be emitted twice and
// in-window reordering must be repaired.
func fixtureRTPDupReorder() fixtureResult {
	mk := func(seq uint16, ts uint32) rtp.Packet {
		return rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 1}, Payload: []byte{byte(seq)}}
	}
	var pkts []rtp.Packet
	for i := uint16(0); i < 20; i++ {
		pkts = append(pkts, mk(i, uint32(i)*3000))
		if i == 3 || i == 4 {
			pkts = append(pkts, mk(i, uint32(i)*3000)) // duplicates
		}
	}
	pkts[6], pkts[7] = pkts[7], pkts[6] // in-window swap (after dup inserts)
	frames, stats, err := media.ProcessRTP(pkts, media.RTPConfig{ClockRate: 90000, ReorderWindow: 8, GapThreshold: 3})
	detected := err == nil && stats.Duplicates == 2 && stats.Lost == 0
	seen := map[int64]bool{}
	ordered := len(frames) == 20
	for i, f := range frames {
		if seen[f.ExtSeq] || (i > 0 && f.ExtSeq != frames[i-1].ExtSeq+1) {
			ordered = false
		}
		seen[f.ExtSeq] = true
	}
	detail := fmt.Sprintf("dups=%d lost=%d frames=%d", stats.Duplicates, stats.Lost, len(frames))
	return fixtureResult{"rtp-duplicate-reorder", detected, ordered, detail}
}

// fixtureRTPSeqWrap: sequence and timestamp wrap must be extended, and a
// backwards timestamp must be flagged while frame times stay monotonic.
func fixtureRTPSeqWrap() fixtureResult {
	mk := func(seq uint16, ts uint32) rtp.Packet {
		return rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 2}, Payload: []byte{byte(seq)}}
	}
	var pkts []rtp.Packet
	seq := uint16(65530)
	for i := uint32(0); i < 16; i++ {
		ts := uint32(0xfffff000) + i*3000 // timestamp wraps mid-stream
		if i == 12 {
			ts -= 4500 // timestamp anomaly: jumps backwards
		}
		pkts = append(pkts, mk(seq, ts))
		seq++
	}
	frames, stats, err := media.ProcessRTP(pkts, media.RTPConfig{ClockRate: 90000, ReorderWindow: 8, GapThreshold: 3})
	detected := err == nil && stats.TimestampAnomalies > 0
	mono := len(frames) == 16
	for i := 1; i < len(frames); i++ {
		if frames[i].TimeNanos <= frames[i-1].TimeNanos {
			mono = false
		}
	}
	wrapOK := len(frames) == 16 && frames[15].ExtSeq-frames[0].ExtSeq == 15
	detail := fmt.Sprintf("anomalies=%d frames=%d ext_range=%d..%d",
		stats.TimestampAnomalies, len(frames), frames[0].ExtSeq, frames[len(frames)-1].ExtSeq)
	return fixtureResult{"rtp-sequence-wrap", detected, mono && wrapOK, detail}
}

// fixtureCorruptCheckpoint: a corrupt checkpoint falls back to the last
// verified stage; downstream artifacts are discarded and rebuilt
// deterministically.
func fixtureCorruptCheckpoint(out string) fixtureResult {
	dir := filepath.Join(out, "corrupt-checkpoint")
	ran := map[string]int{}
	mkStage := func(name string) pipeline.Stage {
		return pipeline.Stage{Name: name, Run: func(deps map[string]json.RawMessage) (any, error) {
			ran[name]++
			return map[string]string{"stage": name}, nil
		}}
	}
	stages := []pipeline.Stage{mkStage("ingest"), mkStage("render"), mkStage("manifest")}
	store, err := pipeline.NewStore(filepath.Join(dir, "cp"))
	if err != nil {
		return fixtureResult{"corrupt-checkpoint", false, false, err.Error()}
	}
	r := &pipeline.Runner{Store: store, InputDigest: "in", ParamsDigest: "p"}
	clean, err := r.Run(stages)
	if err != nil {
		return fixtureResult{"corrupt-checkpoint", false, false, err.Error()}
	}
	if err := os.WriteFile(filepath.Join(dir, "cp", "render.json"), []byte("{corrupt"), 0o644); err != nil {
		return fixtureResult{"corrupt-checkpoint", false, false, err.Error()}
	}
	resumed, err := r.Run(stages)
	if err != nil {
		return fixtureResult{"corrupt-checkpoint", false, false, err.Error()}
	}
	detected := resumed.ResumeFrom == "render"
	recovered := ran["ingest"] == 1 && ran["render"] == 2 && ran["manifest"] == 2 &&
		resumed.Provenance.ChainDigest == clean.Provenance.ChainDigest
	detail := fmt.Sprintf("resume_from=%s runs=%v chain_stable=%t",
		resumed.ResumeFrom, ran, resumed.Provenance.ChainDigest == clean.Provenance.ChainDigest)
	return fixtureResult{"corrupt-checkpoint", detected, recovered, detail}
}
