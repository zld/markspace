package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"replaycheck/internal/clip"
	"replaycheck/internal/fault"
	"replaycheck/internal/wav"
)

// report is the JSON printed on stdout. Field order is fixed.
type report struct {
	Normal      scenarioReport `json:"normal"`
	Damaged     scenarioReport `json:"damaged"`
	Recovery    recoveryReport `json:"recovery"`
	PartialTail tailReport     `json:"partial_tail"`
	Checks      []checkResult  `json:"checks"`
	Passed      bool           `json:"passed"`
}

type tailReport struct {
	CompleteFrames int   `json:"complete_frames"`
	TrailingBytes  int   `json:"trailing_bytes"`
	BadOffset      int64 `json:"bad_offset"`
	BadTick        int64 `json:"bad_tick"`
}

type scenarioReport struct {
	Events            []eventReport `json:"events"`
	TotalFrames       int64         `json:"total_audio_frames"`
	PacketsReceived   int           `json:"packets_received"`
	PacketsDuplicate  int           `json:"packets_duplicate"`
	PacketsReordered  int           `json:"packets_reordered"`
	PacketsLost       int           `json:"packets_lost"`
	KeyframeTicks     []int64       `json:"keyframe_ticks"`
	KeyframeIntervals []int64       `json:"keyframe_intervals"`
	DamagedOffsets    []int64       `json:"damaged_offsets"`
	DamagedGaps       []gapReport   `json:"damaged_gaps"`
	MaxAlignErrMs     float64       `json:"max_alignment_error_ms"`
	ManifestHash      string        `json:"manifest_hash"`
	ManifestPath      string        `json:"manifest_path"`
}

type eventReport struct {
	Name        string  `json:"name"`
	AudioFrames int64   `json:"audio_frames"`
	AudioPeak   string  `json:"audio_peak"`
	VideoFirst  int     `json:"video_first_sample"`
	VideoLast   int     `json:"video_last_sample"`
	LeadInMs    int64   `json:"lead_in_ms"`
	AlignErrMs  float64 `json:"alignment_error_ms"`
	ClipSHA256  string  `json:"clip_sha256"`
	FrameHash   string  `json:"frame_hash"`
	PixelMean   string  `json:"pixel_mean"`
	Damaged     bool    `json:"damaged"`
}

type gapReport struct {
	Source     string `json:"source"`
	StartTick  int64  `json:"start_tick"`
	EndTick    int64  `json:"end_tick"`
	ByteOffset int64  `json:"byte_offset"`
}

type recoveryReport struct {
	Interrupted       bool     `json:"interrupted"`
	ResumeStage       string   `json:"resume_stage"`
	LoadedStages      []string `json:"loaded_stages"`
	CompletedStages   []string `json:"completed_stages"`
	ManifestHash      string   `json:"manifest_hash"`
	UninterruptedHash string   `json:"uninterrupted_hash"`
}

type checkResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func (r *report) OK() bool { return r.Passed }

func (r *report) emit(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

type selfTest struct {
	dir  string
	task string
	fs   *fixtureSet
	rep  *report
}

// runSelfTest builds fixtures and executes the three scenarios.
func runSelfTest(dir, task string) (*report, error) {
	st := &selfTest{dir: dir, task: task, rep: &report{}}
	fs, err := buildFixtures(dir)
	if err != nil {
		return nil, err
	}
	st.fs = fs

	st.fs.TruncWAVSize = int64(len(mustReadFile(st.fs.TruncWAV)))
	cutImg := mustReadFile(st.fs.CutMP4)
	st.fs.CutMP4Size = int64(len(cutImg))
	st.fs.CutMP4BadOffset = firstMissingSampleOffset(cutImg, fixtureSampleSize)

	normal, err := st.scenario("normal", fs.NormalWAV, fs.NormalMP4, fs.DupTrace)
	if err != nil {
		return nil, fmt.Errorf("normal scenario: %w", err)
	}
	st.rep.Normal = normal

	damaged, err := st.scenario("damaged", fs.TruncWAV, fs.CutMP4, fs.BurstTrace)
	if err != nil {
		return nil, fmt.Errorf("damaged scenario: %w", err)
	}
	st.rep.Damaged = damaged

	recovery, err := st.recovery()
	if err != nil {
		return nil, fmt.Errorf("recovery scenario: %w", err)
	}
	st.rep.Recovery = recovery

	tail, err := inspectPartialTail(fs.PartialWAV)
	if err != nil {
		return nil, fmt.Errorf("partial tail: %w", err)
	}
	st.rep.PartialTail = tail
	st.check("incomplete trailing sample frame localised",
		tail.TrailingBytes > 0 && tail.BadOffset > 0,
		fmt.Sprintf("%d trailing bytes at offset %d (tick %d)",
			tail.TrailingBytes, tail.BadOffset, tail.BadTick))
	st.check("incomplete trailing frame is not padded into a frame",
		tail.CompleteFrames == sampleRate,
		fmt.Sprintf("%d complete frames, want %d", tail.CompleteFrames, sampleRate))

	st.check("normal has no damaged output", !anyDamaged(normal.Events),
		"all normal fragments are intact")
	st.check("normal alignment within one audio sample plus one frame",
		normal.MaxAlignErrMs <= st.alignLimitMs(),
		fmt.Sprintf("max %.3fms <= %.3fms", normal.MaxAlignErrMs, st.alignLimitMs()))
	st.check("damaged scenario localises truncation offset",
		len(damaged.DamagedOffsets) > 0, fmt.Sprintf("offsets %v", damaged.DamagedOffsets))
	st.check("damaged scenario points exactly at the truncated wav byte",
		hasOffset(damaged.DamagedOffsets, st.fs.TruncWAVSize),
		fmt.Sprintf("want %d in %v", st.fs.TruncWAVSize, damaged.DamagedOffsets))
	st.check("damaged scenario points exactly at the missing mp4 sample",
		hasOffset(damaged.DamagedOffsets, st.fs.CutMP4BadOffset),
		fmt.Sprintf("want %d in %v", st.fs.CutMP4BadOffset, damaged.DamagedOffsets))
	st.check("damaged scenario flags lost packets", damaged.PacketsLost > 0,
		fmt.Sprintf("%d packets lost", damaged.PacketsLost))
	st.check("damaged scenario marks the missing-packet interval",
		hasSourceGap(damaged.DamagedGaps, "rtp"),
		fmt.Sprintf("gaps %v", damaged.DamagedGaps))
	st.check("normal scenario records no damage gaps", len(normal.DamagedGaps) == 0,
		fmt.Sprintf("gaps %v", normal.DamagedGaps))
	st.check("duplicate and reorder counted",
		normal.PacketsDuplicate > 0 && normal.PacketsReordered > 0,
		fmt.Sprintf("dup=%d reordered=%d", normal.PacketsDuplicate, normal.PacketsReordered))
	st.check("recovery manifest matches uninterrupted", recovery.ManifestHash == recovery.UninterruptedHash,
		recovery.ManifestHash+" vs "+recovery.UninterruptedHash)
	st.check("resume started from the first incomplete stage",
		recovery.ResumeStage == "repair" && len(recovery.LoadedStages) == 3,
		fmt.Sprintf("resume=%s loaded=%v", recovery.ResumeStage, recovery.LoadedStages))
	st.check("keyframe interval reported", len(normal.KeyframeIntervals) > 0,
		fmt.Sprintf("%d intervals", len(normal.KeyframeIntervals)))

	st.rep.Passed = allOK(st.rep.Checks)
	return st.rep, nil
}

func (st *selfTest) alignLimitMs() float64 {
	// One audio sample period plus one video frame at 30 fps.
	oneSample := 1000.0 / float64(sampleRate)
	oneFrame := 1000.0 / float64(fps)
	return oneSample + oneFrame
}

func (st *selfTest) check(name string, ok bool, detail string) {
	st.rep.Checks = append(st.rep.Checks, checkResult{Name: name, OK: ok, Detail: detail})
}

// scenario runs the pipeline once for the given inputs and turns the result
// into a report.
func (st *selfTest) scenario(name, wavPath, mp4Path string, trace [][]byte) (scenarioReport, error) {
	cfg := RunConfig{
		TaskKey:   st.task + "/" + name,
		WAVPath:   wavPath,
		MP4Path:   mp4Path,
		Events:    st.eventsFor(mp4Path),
		RTPTrace:  trace,
		OutDir:    filepath.Join(st.dir, "out", name),
		StateDir:  filepath.Join(st.dir, "state"),
		CkptDir:   filepath.Join(st.dir, "ckpt"),
		ClockRate: videoRate,
	}
	res, err := Run(cfg)
	if err != nil {
		return scenarioReport{}, err
	}
	out := scenarioReport{
		PacketsReceived:   res.Transport.Received,
		PacketsDuplicate:  res.Transport.Duplicate,
		PacketsReordered:  res.Transport.Reordered,
		PacketsLost:       res.Transport.Lost,
		KeyframeTicks:     res.KeyframeTicks,
		KeyframeIntervals: res.Intervals,
		ManifestPath:      res.ManifestPath,
	}
	if res.Manifest != nil {
		out.ManifestHash = res.Manifest.Hash
	}
	for _, c := range res.Clips {
		out.TotalFrames += c.AudioFrames
		event := eventByName(cfg.Events, c.Event)
		errMs := alignmentErrMs(event, c)
		er := eventReport{
			Name:        c.Event,
			AudioFrames: c.AudioFrames,
			AudioPeak:   c.AudioPeak,
			VideoFirst:  c.VideoFirst,
			VideoLast:   c.VideoLast,
			LeadInMs:    c.LeadInMs,
			ClipSHA256:  c.ClipSHA256,
			FrameHash:   c.FrameHash,
			PixelMean:   c.PixelMean,
			Damaged:     c.Damaged,
			AlignErrMs:  errMs,
		}
		if errMs > out.MaxAlignErrMs {
			out.MaxAlignErrMs = errMs
		}
		out.Events = append(out.Events, er)
	}
	seenOff := make(map[int64]bool)
	addOff := func(off int64) {
		if off >= 0 && !seenOff[off] {
			seenOff[off] = true
			out.DamagedOffsets = append(out.DamagedOffsets, off)
		}
	}
	for _, g := range res.Gaps {
		addOff(g.ByteOffset)
		out.DamagedGaps = append(out.DamagedGaps, gapReport{
			Source:     g.Source,
			StartTick:  g.Start,
			EndTick:    g.End,
			ByteOffset: g.ByteOffset,
		})
	}
	addOff(res.FirstBadByte)
	sort.Slice(out.DamagedOffsets, func(i, j int) bool {
		return out.DamagedOffsets[i] < out.DamagedOffsets[j]
	})
	return out, nil
}

// alignmentErrMs sums the two deliberate deviations from the requested
// start: the audio boundary is floored to a whole sample frame, and the
// video boundary is moved back to the nearest keyframe. Both are recorded,
// so the reported error is the actual deviation, not a worst case.
func alignmentErrMs(ev clip.Event, c ClipStats) float64 {
	audioStartTick := c.AudioStart * int64(videoRate) / int64(sampleRate)
	audioDevTicks := ev.StartTick - audioStartTick
	if audioDevTicks < 0 {
		audioDevTicks = -audioDevTicks
	}
	audioMs := float64(audioDevTicks) * 1000.0 / float64(videoRate)
	return audioMs + float64(c.LeadInMs)
}

// eventsFor keeps the damaged scenario's events inside the readable window
// so the cut stage exercises damage rather than failing validation.
func (st *selfTest) eventsFor(mp4Path string) []clip.Event {
	ev := eventSet()
	if mp4Path == st.fs.CutMP4 {
		return []clip.Event{
			{Name: "opening", StartTick: 90000, EndTick: 150000},
			{Name: "tail", StartTick: 240000, EndTick: 330000},
		}
	}
	if mp4Path == st.fs.TruncWAV {
		// Audio is ~3.95s after truncation; keep the last event inside it.
		return []clip.Event{
			{Name: "opening", StartTick: 90000, EndTick: 150000},
			{Name: "clutch", StartTick: 150000, EndTick: 225000},
			{Name: "final", StartTick: 270000, EndTick: 330000},
		}
	}
	return ev
}

func (st *selfTest) recovery() (recoveryReport, error) {
	var rep recoveryReport
	// First run stops right after the scan stage.
	cfgA := RunConfig{
		TaskKey:   st.task + "/recovery",
		WAVPath:   st.fs.NormalWAV,
		MP4Path:   st.fs.NormalMP4,
		Events:    eventSet(),
		RTPTrace:  st.fs.CleanTrace,
		OutDir:    filepath.Join(st.dir, "out", "recovery"),
		StateDir:  filepath.Join(st.dir, "state"),
		CkptDir:   filepath.Join(st.dir, "ckpt"),
		ClockRate: videoRate,
		StopAfter: "scan",
	}
	if _, err := Run(cfgA); err != nil {
		return rep, err
	}
	rep.Interrupted = true

	// Second run resumes from repair and completes.
	cfgB := cfgA
	cfgB.StopAfter = ""
	res, err := Run(cfgB)
	if err != nil {
		return rep, err
	}
	rep.ResumeStage = res.StartStage
	rep.LoadedStages = res.LoadedStages
	rep.CompletedStages = res.Completed
	if res.Manifest != nil {
		rep.ManifestHash = res.Manifest.Hash
	} else if res.ManifestPath != "" {
		data, err := os.ReadFile(res.ManifestPath)
		if err != nil {
			return rep, err
		}
		rep.ManifestHash = manifestHashFromFile(data)
	}

	// Independent uninterrupted run with the SAME task key and inputs, but a
	// fresh state directory: it must produce the same manifest bytes.
	cfgC := cfgB
	cfgC.OutDir = filepath.Join(st.dir, "out", "recovery-uninterrupted")
	cfgC.StateDir = filepath.Join(st.dir, "state-uninterrupted")
	cfgC.CkptDir = filepath.Join(st.dir, "ckpt-uninterrupted")
	resC, err := Run(cfgC)
	if err != nil {
		return rep, err
	}
	if resC.Manifest != nil {
		rep.UninterruptedHash = resC.Manifest.Hash
	}
	return rep, nil
}

func manifestHashFromFile(data []byte) string {
	var doc struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	return doc.Hash
}

// inspectPartialTail parses the trailing-frame fixture and reports the
// incomplete tail exactly as the reader sees it.
func inspectPartialTail(path string) (tailReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return tailReport{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return tailReport{}, err
	}
	lay, err := wav.Parse(f, st.Size())
	if err != nil {
		return tailReport{}, err
	}
	rep := tailReport{
		CompleteFrames: int(lay.Frames),
		TrailingBytes:  lay.PartialTail,
		BadOffset:      -1,
		BadTick:        -1,
	}
	if iss, ok := lay.Issues.First(fault.KindPartialFrame); ok {
		rep.BadOffset = iss.Where.ByteOffset
		rep.BadTick = iss.Where.Tick
	}
	return rep, nil
}

func hasOffset(offsets []int64, want int64) bool {
	for _, o := range offsets {
		if o == want {
			return true
		}
	}
	return false
}

func hasSourceGap(gaps []gapReport, source string) bool {
	for _, g := range gaps {
		if g.Source == source && g.EndTick > g.StartTick {
			return true
		}
	}
	return false
}

func anyDamaged(evs []eventReport) bool {
	for _, e := range evs {
		if e.Damaged {
			return true
		}
	}
	return false
}

func allOK(checks []checkResult) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}

func eventByName(evs []clip.Event, name string) clip.Event {
	for _, e := range evs {
		if e.Name == name {
			return e
		}
	}
	return clip.Event{}
}
