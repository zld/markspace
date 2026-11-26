package clip

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"replaypipe/internal/index"
	"replaypipe/internal/mp4box"
	"replaypipe/internal/timebase"
)

// Written is the outcome of writing one slice to disk.
type Written struct {
	SliceID        string        `json:"slice_id"`
	EventID        string        `json:"event_id"`
	Path           string        `json:"path"`
	SHA256         string        `json:"sha256"`
	Size           int64         `json:"size"`
	VideoSamples   int           `json:"video_samples"`
	AudioSamples   int           `json:"audio_samples"`
	StartKeyframe  int           `json:"start_keyframe"`
	EventStartNS   int64         `json:"event_start_ns"`
	AlignedStartNS int64         `json:"aligned_start_ns"`
	Quality        QualityReport `json:"quality"`
	Reused         bool          `json:"reused"`
}

// Writer copies stored sample bytes into per-event MP4 fragments. Samples are
// never re-encoded: video is cut on a sync sample and audio is cut to the
// covering range.
type Writer struct {
	Media *index.Media
	File  *os.File
	Dir   string
	// MaxOutBytes is the hard delivery ceiling for one fragment. Zero means
	// the default budget ceiling. Exceeding it fails the slice and publishes
	// nothing.
	MaxOutBytes uint64
	// Opened records slice ids whose file was opened by this run.
	Opened map[string]bool
}

// NewWriter opens the source media once for sample copying.
func NewWriter(media *index.Media, dir string) (*Writer, *timebase.Error) {
	f, err := os.Open(media.Path)
	if err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "open media")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.Close()
		return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "create clip dir")
	}
	return &Writer{Media: media, File: f, Dir: dir, Opened: map[string]bool{}}, nil
}

// Close releases the source file.
func (w *Writer) Close() error {
	if w.File != nil {
		return w.File.Close()
	}
	return nil
}

// WriteSlice muxes one planned slice. The output is written to a temp file and
// renamed only after a successful re-parse, so a failed slice never replaces a
// good asset.
func (w *Writer) WriteSlice(s Slice) (*Written, *timebase.Error) {
	video := w.Media.VideoTrack()
	if video == nil {
		return nil, timebase.Newf(timebase.CodeQualityGate, 0, -1, "no video track to cut")
	}
	if s.VideoStart < 0 || s.VideoEnd > len(video.Samples) || s.VideoStart >= s.VideoEnd {
		return nil, timebase.Newf(timebase.CodeEventOutOfRange, video.ID, -1,
			"slice %q sample range [%d,%d) invalid", s.ID, s.VideoStart, s.VideoEnd)
	}
	vSamples, e := w.copyRange(video, s.VideoStart, s.VideoEnd)
	if e != nil {
		return nil, e
	}
	tracks := []mp4box.Track{{
		ID: video.ID, Handler: "vide", Timescale: video.Timescale,
		Width: video.Width, Height: video.Height, Codec: safeCodec(video, "avc1"),
		AvcC:    avcConfig(video),
		Samples: vSamples,
	}}
	audioCount := 0
	if audio := w.Media.AudioTrack(); audio != nil && s.AudioEnd > s.AudioStart {
		aSamples, e := w.copyRange(audio, s.AudioStart, s.AudioEnd)
		if e != nil {
			return nil, e
		}
		audioCount = len(aSamples)
		tracks = append(tracks, mp4box.Track{
			ID: audio.ID, Handler: "soun", Timescale: audio.Timescale,
			SampleRate: audio.SampleRate, Channel: 2, Codec: safeCodec(audio, "mp4a"),
			Samples: aSamples,
		})
	}
	data := mp4box.Build(tracks, mp4box.DefaultOpts())
	limit := w.MaxOutBytes
	if limit == 0 {
		limit = DefaultBudget().MaxOutBytes
	}
	if uint64(len(data)) > limit {
		return nil, timebase.Newf(timebase.CodeOutputTooLarge, video.ID, int64(s.VideoStart),
			"slice %q is %d bytes, delivery ceiling %d", s.ID, len(data), limit)
	}
	path := filepath.Join(w.Dir, s.ID+".mp4")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, video.ID, -1).Wrap(err, "write slice")
	}
	_, err := index.ParseFile(tmp, index.DefaultLimits())
	if err != nil {
		os.Remove(tmp)
		return nil, timebase.AsError(err).WithTrack(video.ID)
	}
	if w.Media.ContentDigest == "" {
		os.Remove(tmp)
		return nil, timebase.Fail(timebase.CodeInternal, video.ID, -1).Wrap(nil, "media content digest missing")
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, timebase.Fail(timebase.CodeInternal, video.ID, -1).Wrap(err, "publish slice")
	}
	sum := sha256.Sum256(data)
	w.Opened[s.ID] = true
	return &Written{
		SliceID: s.ID, EventID: s.EventID, Path: path,
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)),
		VideoSamples: len(vSamples), AudioSamples: audioCount,
		StartKeyframe: s.StartKeyframe, EventStartNS: s.EventStartNS,
		AlignedStartNS: s.AlignedStartNS, Quality: s.Quality, Reused: s.Reused,
	}, nil
}

func (w *Writer) copyRange(tr *index.Track, start, end int) ([]mp4box.Sample, *timebase.Error) {
	out := make([]mp4box.Sample, 0, end-start)
	var nextDTS uint64
	for i := start; i < end && i < len(tr.Samples); i++ {
		smp := tr.Samples[i]
		if uint64(smp.Size) > 256<<20 {
			return nil, timebase.Newf(timebase.CodeByteLimit, tr.ID, smp.Offset,
				"sample %d size %d exceeds copy limit", smp.Index, smp.Size)
		}
		buf := make([]byte, smp.Size)
		if _, err := w.File.ReadAt(buf, smp.Offset); err != nil {
			return nil, timebase.Fail(timebase.CodeBoxTruncated, tr.ID, smp.Offset).Wrap(err, "read sample")
		}
		out = append(out, mp4box.Sample{
			Data: buf, DTS: nextDTS, Duration: uint32(smp.Duration), Sync: smp.Sync,
		})
		nextDTS += uint64(smp.Duration)
	}
	return out, nil
}

func safeCodec(tr *index.Track, fallback string) string {
	c := tr.StringCodec()
	switch c {
	case "":
		return fallback
	default:
		return c
	}
}

// avcConfig extracts the avcC payload from the source stsd so the clip is
// independently decodable. It returns nil when the source has no avcC.
func avcConfig(tr *index.Track) []byte {
	if tr.AvcC != nil {
		return tr.AvcC
	}
	return nil
}

// VerifyReuse confirms that a previously written asset still matches its
// recorded hash. Unchanged assets are reported and left untouched.
func VerifyReuse(root string, prev map[string]string) (map[string]bool, *timebase.Error) {
	ok := map[string]bool{}
	for id, want := range prev {
		path := filepath.Join(root, id)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) == want {
			ok[id] = true
		}
	}
	return ok, nil
}

// RoundTripConfirmed re-reads a written file and reports whether its content
// digest matches the digest the planner predicted for the copied samples.
func RoundTripConfirmed(path string, wantContent string) (bool, *index.Media, error) {
	m, err := index.ParseFile(path, index.DefaultLimits())
	if err != nil {
		return false, nil, err
	}
	if wantContent == "" {
		return true, m, nil
	}
	return m.ContentDigest == wantContent, m, nil
}

var _ = bytes.NewReader
var _ = mp4.BoxTypeFtyp
var _ = fmt.Sprintf
