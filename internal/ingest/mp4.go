package ingest

import (
	"fmt"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
	"mediad/internal/mederr"
)

// MP4Sample is one raw MP4 sample's timing provenance.
type MP4Sample struct {
	RawDTS int64       `json:"raw_dts"`
	RawPTS int64       `json:"raw_pts"`
	Stamp  clock.Stamp `json:"stamp"`
	Delta  uint32      `json:"delta"`
	Size   uint32      `json:"size"`
}

// MP4Track is the probed timeline and supported PCM metadata for one track.
type MP4Track struct {
	ID         uint32      `json:"id"`
	Source     string      `json:"source"`
	Timescale  uint32      `json:"timescale"`
	Duration   uint64      `json:"duration_ticks"`
	DurationNs int64       `json:"duration_ns"`
	Channels   int         `json:"channels,omitempty"`
	SampleRate int         `json:"sample_rate,omitempty"`
	Samples    []MP4Sample `json:"samples,omitempty"`
	PCMOffset  int64       `json:"-"`
	PCMBytes   int64       `json:"-"`
}

// MP4File is the bounded result of reading MP4 track metadata.
type MP4File struct {
	Path       string     `json:"path"`
	Timescale  uint32     `json:"timescale"`
	DurationNs int64      `json:"duration_ns"`
	Tracks     []MP4Track `json:"tracks"`
}

// ReadMP4Timeline uses go-mp4/Probe to read track and sample timing. Raw DTS
// and PTS values are retained in addition to nanosecond conversions.
func ReadMP4Timeline(path string) (*MP4File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, mederr.New(mederr.CatParse, "mp4", -1, -1, false, "open MP4 fixture: %v", err)
	}
	defer f.Close()
	info, err := gomp4.Probe(f)
	if err != nil {
		return nil, mederr.New(mederr.CatParse, "mp4", -1, -1, false, "probe MP4 %s: %v", path, err)
	}
	if len(info.Tracks) == 0 {
		return nil, mederr.New(mederr.CatTimeline, "mp4", -1, -1, false, "MP4 %s has no tracks", path)
	}
	out := &MP4File{Path: path, Timescale: info.Timescale}
	if info.Timescale > 0 {
		out.DurationNs = clock.Scale{Num: int64(info.Timescale), Den: 1}.ToNanos(int64(info.Duration))
	}
	for _, tr := range info.Tracks {
		if tr.Timescale == 0 {
			return nil, mederr.New(mederr.CatTimeline, fmt.Sprintf("mp4:%d", tr.TrackID), -1, -1, false,
				"track has zero media timescale")
		}
		if len(tr.Samples) > MaxSamples {
			return nil, mederr.New(mederr.CatLimit, fmt.Sprintf("mp4:%d", tr.TrackID), -1, -1, false,
				"sample count %d exceeds limit %d", len(tr.Samples), MaxSamples)
		}
		scale := clock.Scale{Num: int64(tr.Timescale), Den: 1}
		t := MP4Track{
			ID:         tr.TrackID,
			Source:     fmt.Sprintf("mp4:%d", tr.TrackID),
			Timescale:  tr.Timescale,
			Duration:   tr.Duration,
			DurationNs: scale.ToNanos(int64(tr.Duration)),
			Samples:    make([]MP4Sample, 0, len(tr.Samples)),
		}
		if tr.MP4A != nil {
			t.Channels = int(tr.MP4A.ChannelCount)
		}
		t.SampleRate = int(tr.Timescale)
		if t.Channels == 0 && len(tr.Samples) > 0 {
			s := tr.Samples[0]
			if s.Size > 0 && s.TimeDelta > 0 {
				if channels := int(s.Size / (2 * s.TimeDelta)); channels > 0 && channels <= 8 {
					t.Channels = channels
				}
			}
		}
		if len(tr.Chunks) > 0 {
			t.PCMOffset = int64(tr.Chunks[0].DataOffset)
		}
		var dts int64
		var pcmBytes int64
		for _, s := range tr.Samples {
			pts := dts + s.CompositionTimeOffset
			t.Samples = append(t.Samples, MP4Sample{
				RawDTS: dts,
				RawPTS: pts,
				Delta:  s.TimeDelta,
				Size:   s.Size,
				Stamp: clock.Stamp{
					Ticks:  pts,
					Scale:  scale,
					Raw:    pts,
					Source: t.Source + ":" + scale.String(),
				},
			})
			dts += int64(s.TimeDelta)
			pcmBytes += int64(s.Size)
		}
		t.PCMBytes = pcmBytes
		if t.DurationNs > MaxDurationNanos {
			return nil, mederr.New(mederr.CatLimit, t.Source, 0, t.DurationNs, false,
				"MP4 track duration exceeds limit")
		}
		out.Tracks = append(out.Tracks, t)
	}
	return out, nil
}

// ReadMP4PCMS16 reads the supported fixture PCM payload for trackID. The
// project only promises its generated s16-in-mp4a fixtures, not all codecs.
func ReadMP4PCMS16(path string, trackID uint32) ([]byte, MP4Track, error) {
	tl, err := ReadMP4Timeline(path)
	if err != nil {
		return nil, MP4Track{}, err
	}
	for _, t := range tl.Tracks {
		if t.ID != trackID {
			continue
		}
		if t.PCMOffset <= 0 || t.PCMBytes <= 0 || t.PCMBytes > MaxFrameBytes*int64(MaxSamples) {
			return nil, t, mederr.New(mederr.CatParse, t.Source, 0, t.DurationNs, false,
				"track has no readable bounded PCM payload")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, t, mederr.New(mederr.CatIO, t.Source, -1, -1, false, "open PCM: %v", err)
		}
		defer f.Close()
		buf := make([]byte, t.PCMBytes)
		n, err := f.ReadAt(buf, t.PCMOffset)
		if err != nil || int64(n) != t.PCMBytes {
			return nil, t, mederr.New(mederr.CatParse, t.Source, 0, t.DurationNs, true,
				"truncated MP4 PCM payload: read %d/%d: %v", n, t.PCMBytes, err)
		}
		return buf, t, nil
	}
	return nil, MP4Track{}, mederr.New(mederr.CatTimeline, fmt.Sprintf("mp4:%d", trackID), -1, -1, false,
		"MP4 track not found")
}
