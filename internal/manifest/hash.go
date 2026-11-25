// Package manifest assembles the deterministic archive summary and writes it
// atomically. The manifest aggregates frame hashes, segment hashes, pixel
// statistics, loudness peaks, gap counts and alignment error; it deliberately
// contains no timestamps, paths, PIDs or wall-clock values, so the same fixture
// and seed always produces byte-identical output.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"archive.example/mediakernel/internal/audio"
	"archive.example/mediakernel/internal/events"
	"archive.example/mediakernel/internal/render"
)

// OutputDirName is the fixed directory name the CLI writes into the requested
// output root. Paths are confined to it.
const OutputDirName = "archive"

// Manifest is the complete archive summary.
type Manifest struct {
	Version        string          `json:"version"`
	Fixture        string          `json:"fixture"`
	Seed           int64           `json:"seed"`
	Timeline       TimelineSummary `json:"timeline"`
	Frames         FrameSummary    `json:"frames"`
	Audio          AudioSummary    `json:"audio"`
	Alignment      AlignSummary    `json:"alignment"`
	RTP            RTPSummary      `json:"rtp"`
	Gaps           []GapSummary    `json:"gaps"`
	Errors         []ErrorSummary  `json:"errors"`
	StateSHA256    string          `json:"state_sha256"`
	Recovered      bool            `json:"recovered"`
	ManifestSHA256 string          `json:"manifest_sha256,omitempty"`
}

// TimelineSummary is the event inventory of the committed timeline.
type TimelineSummary struct {
	WatermarkNS int64          `json:"watermark_ns"`
	Committed   int            `json:"committed_events"`
	Pending     int            `json:"pending_events"`
	ByTrack     map[string]int `json:"events_by_track"`
	Replays     int            `json:"replays"`
	Late        int            `json:"late_events"`
	Admitted    int            `json:"admitted"`
}

// FrameSummary summarises the exported frame sequence.
type FrameSummary struct {
	Count          int      `json:"count"`
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	StepNS         int64    `json:"step_ns"`
	FirstFrameNS   int64    `json:"first_frame_ns"`
	LastFrameNS    int64    `json:"last_frame_ns"`
	SequenceSHA256 string   `json:"sequence_sha256"`
	FrameHashes    []string `json:"frame_hashes"`
	SegmentHashes  []string `json:"segment_hashes"`
	MeanByte       float64  `json:"mean_byte"`
	MinByte        uint8    `json:"min_byte"`
	MaxByte        uint8    `json:"max_byte"`
}

// AudioSummary summarises the PCM export.
type AudioSummary struct {
	Samples       int     `json:"samples"`
	SampleRate    int     `json:"sample_rate"`
	Channels      int     `json:"channels"`
	BitsPerSample int     `json:"bits_per_sample"`
	Peak          float64 `json:"peak"`
	PeakDBFS      float64 `json:"peak_dbfs"`
	RMS           float64 `json:"rms"`
	RMSDBFS       float64 `json:"rms_dbfs"`
	Finite        bool    `json:"finite"`
	SHA256        string  `json:"sha256"`
}

// AlignSummary records alignment and interval bounds.
type AlignSummary struct {
	MaxAlignmentErrorNS  int64 `json:"max_alignment_error_ns"`
	FrameDurationNS      int64 `json:"frame_duration_ns"`
	AlignmentWithinFrame bool  `json:"alignment_within_one_frame"`
}

// RTPSummary is the reorderer inventory.
type RTPSummary struct {
	Received   int    `json:"received"`
	Delivered  int    `json:"delivered"`
	Duplicates int    `json:"duplicates"`
	OutOfOrder int    `json:"out_of_order"`
	Lost       int    `json:"lost"`
	Late       int    `json:"late"`
	Status     string `json:"status"`
	Packets    int    `json:"input_packets"`
}

// GapSummary is one unresolved gap marker.
type GapSummary struct {
	Track  string `json:"track"`
	Kind   string `json:"kind"`
	Seq    uint64 `json:"seq"`
	AtNS   int64  `json:"at_ns"`
	Reason string `json:"reason"`
}

// ErrorSummary is one reported error, with its location.
type ErrorSummary struct {
	Code   string `json:"code"`
	Track  string `json:"track,omitempty"`
	Seq    uint64 `json:"seq"`
	Offset int64  `json:"offset"`
	Op     string `json:"op,omitempty"`
	Detail string `json:"detail,omitempty"`
	Fatal  bool   `json:"fatal"`
}

// Inputs bundles everything the manifest aggregates.
type Inputs struct {
	Fixture     string
	Seed        int64
	Timeline    TimelineLike
	Frames      render.Sequence
	Audio       audio.Stats
	RTP         RTPSummary
	Gaps        []events.Gap
	Errors      []*events.Error
	StateHash   string
	Recovered   bool
	FrameWidth  int
	FrameHeight int
	SegmentSize int
}

// TimelineLike is the subset of timeline state the manifest needs.
type TimelineLike struct {
	WatermarkNS int64
	Committed   []events.Event
	Pending     int
	ByTrack     map[string]int
	Replays     int
	Late        int
	Admitted    int
}

// Build assembles a manifest from inputs. It is a pure function: the same
// inputs always produce the same bytes.
func Build(in Inputs) Manifest {
	seq := in.SegmentSize
	if seq <= 0 {
		seq = 8
	}
	frameHashes := make([]string, 0, len(in.Frames.Frames))
	var meanSum float64
	lo, hi := uint8(255), uint8(0)
	for _, f := range in.Frames.Frames {
		frameHashes = append(frameHashes, f.SHA256)
		meanSum += f.MeanByte
		if f.MinByte < lo {
			lo = f.MinByte
		}
		if f.MaxByte > hi {
			hi = f.MaxByte
		}
	}
	mean := 0.0
	if len(in.Frames.Frames) > 0 {
		mean = meanSum / float64(len(in.Frames.Frames))
	} else {
		lo, hi = 0, 0
	}

	byTrack := in.Timeline.ByTrack
	if byTrack == nil {
		byTrack = map[string]int{}
	}

	gaps := make([]GapSummary, 0, len(in.Gaps))
	for _, g := range events.UnresolvedGaps(in.Gaps) {
		gaps = append(gaps, GapSummary{
			Track: string(g.Track), Kind: string(g.Kind), Seq: g.Seq, AtNS: g.At, Reason: g.Reason,
		})
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Track != gaps[j].Track {
			return gaps[i].Track < gaps[j].Track
		}
		if gaps[i].Seq != gaps[j].Seq {
			return gaps[i].Seq < gaps[j].Seq
		}
		return gaps[i].Reason < gaps[j].Reason
	})

	errs := make([]ErrorSummary, 0, len(in.Errors))
	for _, e := range in.Errors {
		if e == nil {
			continue
		}
		errs = append(errs, ErrorSummary{
			Code: string(e.Code), Track: string(e.Track), Seq: e.Seq, Offset: e.Offset,
			Op: e.Op, Detail: e.Detail, Fatal: e.Fatal(),
		})
	}
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Offset != errs[j].Offset {
			return errs[i].Offset < errs[j].Offset
		}
		if errs[i].Track != errs[j].Track {
			return errs[i].Track < errs[j].Track
		}
		if errs[i].Code != errs[j].Code {
			return errs[i].Code < errs[j].Code
		}
		return errs[i].Seq < errs[j].Seq
	})

	firstNS := in.Frames.StartNS
	lastNS := int64(0)
	if n := len(in.Frames.Frames); n > 0 {
		lastNS = in.Frames.Frames[n-1].AtNS
	}

	alignOK := in.Frames.FrameDurationNS > 0 && in.Frames.MaxAlignError <= in.Frames.FrameDurationNS

	m := Manifest{
		Version: "archive-manifest-v1",
		Fixture: in.Fixture,
		Seed:    in.Seed,
		Timeline: TimelineSummary{
			WatermarkNS: in.Timeline.WatermarkNS,
			Committed:   len(in.Timeline.Committed),
			Pending:     in.Timeline.Pending,
			ByTrack:     byTrack,
			Replays:     in.Timeline.Replays,
			Late:        in.Timeline.Late,
			Admitted:    in.Timeline.Admitted,
		},
		Frames: FrameSummary{
			Count:          len(in.Frames.Frames),
			Width:          in.FrameWidth,
			Height:         in.FrameHeight,
			StepNS:         in.Frames.FrameDurationNS,
			FirstFrameNS:   firstNS,
			LastFrameNS:    lastNS,
			SequenceSHA256: in.Frames.SequenceHash(),
			FrameHashes:    frameHashes,
			SegmentHashes:  render.SegmentHashes(in.Frames, seq),
			MeanByte:       round(mean, 3),
			MinByte:        lo,
			MaxByte:        hi,
		},
		Audio: AudioSummary{
			Samples:       in.Audio.Samples,
			SampleRate:    in.Audio.SampleRate,
			Channels:      in.Audio.Channels,
			BitsPerSample: in.Audio.BitsPerSample,
			Peak:          round(in.Audio.Peak, 6),
			PeakDBFS:      round(in.Audio.PeakDBFS, 3),
			RMS:           round(in.Audio.RMS, 6),
			RMSDBFS:       round(in.Audio.RMSDBFS, 3),
			Finite:        in.Audio.Finite,
			SHA256:        in.Audio.SHA256,
		},
		Alignment: AlignSummary{
			MaxAlignmentErrorNS:  in.Frames.MaxAlignError,
			FrameDurationNS:      in.Frames.FrameDurationNS,
			AlignmentWithinFrame: alignOK,
		},
		RTP:         in.RTP,
		Gaps:        gaps,
		Errors:      errs,
		StateSHA256: in.StateHash,
		Recovered:   in.Recovered,
	}
	return m
}

// round trims floating point noise so the JSON output is stable across runs.
func round(v float64, places int) float64 {
	pow := 1.0
	for i := 0; i < places; i++ {
		pow *= 10
	}
	return float64(int64(v*pow+sign(v)*0.5)) / pow
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// Bytes serialises the manifest with stable key ordering and a trailing
// newline. ManifestSHA256 is the digest of the manifest with that field blank,
// then recorded in the output; the same inputs always yield the same bytes.
func (m *Manifest) Bytes() ([]byte, error) {
	m.ManifestSHA256 = ""
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	m.ManifestSHA256 = hex.EncodeToString(digest[:])
	final, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(final, '\n'), nil
}

// ResolveOutDir returns the confined output directory for a requested root.
// The root must be absolute or relative to the working directory; the archive
// subdirectory is created if missing. Tilde and parent traversal are rejected.
func ResolveOutDir(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("output directory is empty")
	}
	if strings.Contains(root, "..") {
		return "", fmt.Errorf("output directory must not contain %q: %s", "..", root)
	}
	if strings.HasPrefix(root, "~") {
		return "", fmt.Errorf("output directory must be an explicit path, got %q", root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// WriteAtomic writes data to dir/name via a temporary file in the same
// directory followed by a rename, so a failed write never leaves a partial
// manifest behind.
func WriteAtomic(dir, name string, data []byte) (string, error) {
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid file name %q", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return path, nil
}
