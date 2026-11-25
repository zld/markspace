package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"replaycheck/internal/checkpoint"
	"replaycheck/internal/clip"
	"replaycheck/internal/fault"
	"replaycheck/internal/manifest"
	"replaycheck/internal/metrics"
	"replaycheck/internal/repair"
	rtr "replaycheck/internal/rtp"
	"replaycheck/internal/state"
	"replaycheck/internal/stream"
	"replaycheck/internal/wav"
)

// TransportStats is the checkpointed RTP summary.
type TransportStats struct {
	Received  int `json:"received"`
	Duplicate int `json:"duplicate"`
	Reordered int `json:"reordered"`
	Lost      int `json:"lost"`
	Wraps     int `json:"wraps"`
	Late      int `json:"late"`
	Spans     int `json:"spans"`
	// LostTimestamps are the timestamps on either side of each committed
	// gap, in RTP clock ticks, used for timeline placement.
	LostTimestamps [][2]uint32 `json:"lost_timestamps"`
}

// ScanStats is the checkpointed scan summary.
type ScanStats struct {
	Samples        int     `json:"samples"`
	Keyframes      int     `json:"keyframes"`
	FirstBadOffset int64   `json:"first_bad_offset"`
	FirstBadTick   int64   `json:"first_bad_tick"`
	KeyframeTicks  []int64 `json:"keyframe_ticks"`
	Intervals      []int64 `json:"keyframe_intervals"`
	Duration       int64   `json:"duration_ticks"`
	Timescale      uint32  `json:"timescale"`
}

// ClipStats is the checkpointed cut geometry and content summary.
type ClipStats struct {
	TaskKey     string `json:"task_key"`
	Event       string `json:"event"`
	AudioStart  int64  `json:"audio_start_frame"`
	AudioEnd    int64  `json:"audio_end_frame"`
	AudioFrames int64  `json:"audio_frames"`
	VideoFirst  int    `json:"video_first"`
	VideoLast   int    `json:"video_last"`
	LeadInMs    int64  `json:"lead_in_ms"`
	LeadInTick  int64  `json:"lead_in_ticks"`
	ClipSHA256  string `json:"clip_sha256"`
	FrameHash   string `json:"frame_hash"`
	PixelMean   string `json:"pixel_mean"`
	AudioPeak   string `json:"audio_peak"`
	Damaged     bool   `json:"damaged"`
	Bytes       int64  `json:"bytes"`
	Path        string `json:"path"`
}

// RunConfig describes one pipeline run.
type RunConfig struct {
	TaskKey  string
	WAVPath  string
	MP4Path  string
	Events   []clip.Event
	RTPTrace [][]byte
	OutDir   string
	StateDir string
	CkptDir  string
	// StopAfter ends the run just after the named stage, simulating an
	// interruption. Empty runs to completion.
	StopAfter string
	// ClockRate is the RTP timestamp clock.
	ClockRate int
}

// RunResult reports what a run produced.
type RunResult struct {
	TaskKey       string
	InputDigest   string
	StartStage    string
	Completed     []string
	LoadedStages  []string
	Manifest      *manifest.Document
	ManifestPath  string
	Clips         []ClipStats
	KeyframeTicks []int64
	Intervals     []int64
	Transport     TransportStats
	FirstBadByte  int64
	Gaps          []repair.Gap
	Scan          ScanStats
}

// Run executes the pipeline, resuming from the persisted frontier when a
// state file already exists.
func Run(cfg RunConfig) (*RunResult, error) {
	if cfg.ClockRate <= 0 {
		cfg.ClockRate = 90000
	}
	if cfg.TaskKey == "" {
		return nil, fmt.Errorf("empty task key")
	}
	for _, ev := range cfg.Events {
		if ev.Name == "" {
			return nil, fmt.Errorf("event with empty name")
		}
	}
	digest, err := inputDigest(cfg)
	if err != nil {
		return nil, err
	}
	st, err := state.Open(cfg.StateDir, cfg.TaskKey, digest)
	if err != nil {
		return nil, err
	}
	if err := st.ValidateOrder(); err != nil {
		return nil, err
	}
	ptr := checkpoint.New(cfg.CkptDir)
	res := &RunResult{TaskKey: cfg.TaskKey, InputDigest: digest}

	frontier, err := st.ResumeStage()
	if err != nil {
		return nil, err
	}
	stoppedMidway := false
	if frontier == "" {
		// Everything is already recorded; re-verify the final checkpoint.
		frontier = state.StageManifest
		stoppedMidway = true
	}
	res.StartStage = frontier

	var rtpStats TransportStats
	var scanStats ScanStats
	var gaps []repair.Gap
	var clips []ClipStats

	// Load checkpoints for stages before the frontier. Any checkpoint that
	// no longer matches the inputs stops the run before media is touched.
	for _, name := range state.Order {
		if name == frontier {
			break
		}
		if err := loadStage(ptr, cfg.TaskKey, digest, name, &rtpStats, &scanStats, &gaps, &clips); err != nil {
			return nil, fmt.Errorf("resume before %s: %w", name, err)
		}
		res.LoadedStages = append(res.LoadedStages, name)
	}

	wavR, err := wav.Open(cfg.WAVPath)
	if err != nil {
		return nil, err
	}
	defer wavR.Close()
	mp4F, err := os.Open(cfg.MP4Path)
	if err != nil {
		return nil, err
	}
	defer mp4F.Close()
	mp4Size, err := mp4F.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}

	runStage := func(stage string) error {
		switch stage {
		case state.StageIngest:
			return ptr.Write(cfg.TaskKey, stage, digest, map[string]any{
				"wav_bytes": wavR.Layout.Available,
				"mp4_bytes": mp4Size,
			})
		case state.StageTransport:
			rtpStats = analyzeRTP(cfg.RTPTrace, cfg.ClockRate)
			return ptr.Write(cfg.TaskKey, stage, digest, rtpStats)
		case state.StageScan:
			info, err := stream.ParseFile(mp4F, cfg.MP4Path, mp4Size)
			if err != nil {
				return err
			}
			scanStats = ScanStats{
				Samples:        len(info.Track.Samples),
				Keyframes:      len(info.KeyframeTicks),
				FirstBadOffset: info.FirstBadOffset,
				FirstBadTick:   info.FirstBadTick,
				KeyframeTicks:  info.KeyframeTicks,
				Intervals:      info.KeyframeIntervals,
				Duration:       info.Duration,
				Timescale:      info.Timescale,
			}
			res.Scan = scanStats
			return ptr.Write(cfg.TaskKey, stage, digest, scanStats)
		case state.StageRepair:
			gaps = localiseGaps(rtpStats, wavR.Layout, scanStats, cfg.ClockRate, wavR.Layout.Format.SampleRate)
			return ptr.Write(cfg.TaskKey, stage, digest, gaps)
		case state.StageCut:
			built, err := cutAll(cfg, wavR, mp4F, gaps)
			if err != nil {
				return err
			}
			clips = built
			return ptr.Write(cfg.TaskKey, stage, digest, clips)
		case state.StageManifest:
			doc, err := mergeManifest(cfg, digest, clips)
			if err != nil {
				return err
			}
			path := filepath.Join(cfg.OutDir, "manifest.json")
			if err := manifest.WriteAtomic(path, doc); err != nil {
				return err
			}
			res.Manifest = doc
			res.ManifestPath = path
			return ptr.Write(cfg.TaskKey, stage, digest, map[string]string{
				"manifest_hash": doc.Hash,
				"path":          path,
			})
		}
		return fmt.Errorf("unknown stage %q", stage)
	}

	started := stoppedMidway
	for _, name := range state.Order {
		if !started && name == frontier {
			started = true
		}
		if !started {
			continue
		}
		if err := runStage(name); err != nil {
			return nil, fmt.Errorf("stage %s: %w", name, err)
		}
		if err := st.MarkStage(name, ""); err != nil {
			return nil, err
		}
		res.Completed = append(res.Completed, name)
		if cfg.StopAfter != "" && name == cfg.StopAfter {
			break
		}
	}

	res.Transport = rtpStats
	res.KeyframeTicks = scanStats.KeyframeTicks
	res.Intervals = scanStats.Intervals
	res.FirstBadByte = scanStats.FirstBadOffset
	res.Gaps = gaps
	res.Clips = clips
	res.Scan = scanStats
	if res.Manifest == nil && st.Completed(state.StageManifest) {
		var persisted struct {
			ManifestHash string `json:"manifest_hash"`
			Path         string `json:"path"`
		}
		if err := ptr.Load(cfg.TaskKey, state.StageManifest, digest, &persisted); err != nil {
			return nil, err
		}
		res.ManifestPath = persisted.Path
	}
	return res, nil
}

func loadStage(ptr *checkpoint.Sta, key, digest, stage string, rtpStats *TransportStats, scanStats *ScanStats, gaps *[]repair.Gap, clips *[]ClipStats) error {
	switch stage {
	case state.StageIngest:
		var v map[string]any
		return ptr.Load(key, stage, digest, &v)
	case state.StageTransport:
		return ptr.Load(key, stage, digest, rtpStats)
	case state.StageScan:
		return ptr.Load(key, stage, digest, scanStats)
	case state.StageRepair:
		return ptr.Load(key, stage, digest, gaps)
	case state.StageCut:
		return ptr.Load(key, stage, digest, clips)
	case state.StageManifest:
		var v struct {
			ManifestHash string `json:"manifest_hash"`
			Path         string `json:"path"`
		}
		return ptr.Load(key, stage, digest, &v)
	}
	return fmt.Errorf("unknown stage %q", stage)
}

// inputDigest pins a run to its exact inputs: media bytes, event list, RTP
// trace and task key. Media is streamed in bounded blocks so a large
// programme does not materialise in memory.
func inputDigest(cfg RunConfig) (string, error) {
	h := metrics.NewSampleHasher()
	for _, path := range []string{cfg.WAVPath, cfg.MP4Path} {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		buf := make([]byte, 256<<10)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				h.AddSample(buf[:n])
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				return "", err
			}
		}
		f.Close()
	}
	var ev strings.Builder
	for _, e := range cfg.Events {
		fmt.Fprintf(&ev, "%s|%d|%d\n", e.Name, e.StartTick, e.EndTick)
	}
	h.AddSample([]byte(ev.String()))
	for _, p := range cfg.RTPTrace {
		h.AddSample(p)
	}
	h.AddSample([]byte(cfg.TaskKey))
	return h.Sum(), nil
}

func analyzeRTP(trace [][]byte, clockRate int) TransportStats {
	rr := rtr.New(rtr.Config{Window: 64, ClockRate: clockRate})
	for _, raw := range trace {
		p, err := rtr.Decode(raw)
		if err != nil {
			continue
		}
		rr.Push(p)
	}
	out := rr.Finish()
	ts := TransportStats{
		Received:  out.Received,
		Duplicate: out.Duplicate,
		Reordered: out.Reordered,
		Lost:      out.Lost,
		Wraps:     out.Wraps,
		Late:      out.Late,
		Spans:     len(out.Committed),
	}
	for _, sp := range out.Committed {
		if sp.Lost > 0 {
			ts.LostTimestamps = append(ts.LostTimestamps, [2]uint32{sp.GapStartTick, sp.GapEndTick})
		}
	}
	return ts
}

// localiseGaps converts every defect observation into a half-open interval
// on the shared timeline. WAV damage starts at the first frame that is not
// backed by file bytes; MP4 damage starts at the decode time of the first
// sample whose payload is missing; RTP burst damage spans the timestamps of
// the packets on either side of the committed gap.
func localiseGaps(ts TransportStats, lay *wav.Layout, scan ScanStats, clockRate int, sampleRate int) []repair.Gap {
	var gaps []repair.Gap
	if lay != nil && lay.FirstBadOffset >= 0 && sampleRate > 0 {
		// Place the WAV boundary on the shared tick base: frames -> ticks,
		// scaled by the video timescale when the two differ.
		base := int64(sampleRate)
		if scan.Timescale > 0 {
			base = int64(scan.Timescale)
		}
		frameTicks := func(frames int64) int64 { return frames * base / int64(sampleRate) }
		start := frameTicks(lay.Frames)
		// The declared data size bounds the damaged interval: readable
		// frames before it, declared frames after it. An incomplete tail
		// declares the same frame count, so the interval is one tick.
		declared := lay.Frames
		if fb := int64(lay.Format.BlockAlign); fb > 0 && lay.DataSize > 0 {
			declared = lay.DataSize / fb
		}
		end := frameTicks(declared)
		if end <= start {
			end = start + 1
		}
		kind := fault.KindTruncated
		detail := "wav data ends before its declared size"
		if lay.PartialTail > 0 && lay.DataSize/int64(lay.Format.BlockAlign) == lay.Frames {
			kind = fault.KindPartialFrame
			detail = "wav ends with an incomplete sample frame"
		}
		gaps = append(gaps, repair.Gap{
			Source: "wav", Start: start, End: end,
			ByteOffset: lay.FirstBadOffset,
			Kind:       kind,
			Detail:     detail,
		})
	}
	if scan.FirstBadOffset >= 0 && scan.Timescale > 0 {
		start := scan.FirstBadTick
		if start < 0 {
			start = scan.Duration
		}
		gaps = append(gaps, repair.Gap{
			Source: "mp4", Start: start, End: start + int64(scan.Timescale)/30,
			ByteOffset: scan.FirstBadOffset,
			Kind:       fault.KindUnreadable,
			Detail:     "mp4 sample payload missing",
		})
	}
	for _, pair := range ts.LostTimestamps {
		start := int64(pair[0]) * int64(scan.Timescale) / int64(maxInt(clockRate, 1))
		end := int64(pair[1]) * int64(scan.Timescale) / int64(maxInt(clockRate, 1))
		if end <= start {
			end = start + int64(scan.Timescale)/30
		}
		gaps = append(gaps, repair.Gap{
			Source: "rtp", Start: start, End: end,
			ByteOffset: -1,
			Kind:       fault.KindPacketLoss,
			Detail:     fmt.Sprintf("%d rtp packets missing", ts.Lost),
		})
	}
	return gaps
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func mergeManifest(cfg RunConfig, digest string, clips []ClipStats) (*manifest.Document, error) {
	m := manifest.New(cfg.TaskKey, digest)
	for _, c := range clips {
		err := m.Add(manifest.Clip{
			TaskKey:     c.TaskKey,
			Event:       c.Event,
			Path:        c.Path,
			Bytes:       c.Bytes,
			AudioFrames: c.AudioFrames,
			AudioPeak:   c.AudioPeak,
			VideoFirst:  c.VideoFirst,
			VideoLast:   c.VideoLast,
			LeadInMs:    c.LeadInMs,
			FrameHash:   c.FrameHash,
			PixelMean:   c.PixelMean,
			Damaged:     c.Damaged,
		})
		if err != nil {
			return nil, err
		}
	}
	return m.Seal()
}
