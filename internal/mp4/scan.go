// Package mp4scan scans MP4 files into the shared integer timeline: tracks,
// per-sample durations, sizes, keyframe flags and structured faults for
// anomalous timestamps and corrupt samples.
package mp4scan

import (
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"continuity/internal/timeline"
)

// Sample is one decoded sample-table entry on the track clock.
type Sample struct {
	Index    int
	Start    timeline.Ticks
	Duration timeline.Ticks
	Size     int64
	Key      bool
}

// Track is the scanned result for one MP4 track.
type Track struct {
	ID      int64
	Kind    string // "audio", "video", or the raw handler type
	Clock   timeline.Clock
	Samples []Sample
	Faults  []*timeline.Fault
}

// Duration is the total track duration in track ticks.
func (t *Track) Duration() timeline.Ticks {
	var d timeline.Ticks
	for _, s := range t.Samples {
		d += s.Duration
	}
	return d
}

// TotalBytes sums sample payload sizes.
func (t *Track) TotalBytes() int64 {
	var n int64
	for _, s := range t.Samples {
		n += s.Size
	}
	return n
}

// File is a scanned MP4 file.
type File struct {
	Tracks []*Track
}

// rawTrack accumulates box payloads before sample assembly.
type rawTrack struct {
	id       uint32
	kind     string
	timescale uint32
	deltas   []uint32
	sizes    []uint32
	keys     map[uint32]bool
}

// ScanFile parses an MP4 file's track and sample tables.
func ScanFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var raws []*rawTrack
	var cur *rawTrack
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl():
			return h.Expand()
		case mp4.BoxTypeTrak():
			cur = &rawTrack{keys: map[uint32]bool{}}
			raws = append(raws, cur)
			return h.Expand()
		case mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			cur.id = box.(*mp4.Tkhd).TrackID
		case mp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			cur.kind = string(box.(*mp4.Hdlr).HandlerType[:])
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			cur.timescale = box.(*mp4.Mdhd).Timescale
		case mp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, e := range box.(*mp4.Stts).Entries {
				for i := uint32(0); i < e.SampleCount; i++ {
					cur.deltas = append(cur.deltas, e.SampleDelta)
				}
			}
		case mp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stsz := box.(*mp4.Stsz)
			if stsz.SampleSize != 0 {
				for i := uint32(0); i < stsz.SampleCount; i++ {
					cur.sizes = append(cur.sizes, stsz.SampleSize)
				}
			} else {
				cur.sizes = append(cur.sizes, stsz.EntrySize...)
			}
		case mp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, n := range box.(*mp4.Stss).SampleNumber {
				cur.keys[n] = true
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("mp4scan: %w", err)
	}

	out := &File{}
	for _, r := range raws {
		out.Tracks = append(out.Tracks, r.build())
	}
	return out, nil
}

func (r *rawTrack) build() *Track {
	kind := r.kind
	switch kind {
	case "soun":
		kind = "audio"
	case "vide":
		kind = "video"
	}
	t := &Track{
		ID:    int64(r.id),
		Kind:  kind,
		Clock: timeline.Clock{Timescale: int64(r.timescale)},
	}
	trackName := fmt.Sprintf("%s/mp4#%d", kind, r.id)
	var start timeline.Ticks
	for i, d := range r.deltas {
		var size int64
		if i < len(r.sizes) {
			size = int64(r.sizes[i])
		}
		if d == 0 {
			t.Faults = append(t.Faults, &timeline.Fault{
				Track: trackName, Stage: timeline.StageScan, Offset: int64(i),
				Msg: "anomalous timestamp: zero sample delta",
			})
		}
		if i < len(r.sizes) && r.sizes[i] == 0 {
			t.Faults = append(t.Faults, &timeline.Fault{
				Track: trackName, Stage: timeline.StageScan, Offset: int64(i),
				Msg: "corrupt sample: zero size",
			})
		}
		key := len(r.keys) == 0 || r.keys[uint32(i+1)]
		t.Samples = append(t.Samples, Sample{
			Index: i, Start: start, Duration: timeline.Ticks(d),
			Size: size, Key: key,
		})
		start += timeline.Ticks(d)
	}
	return t
}
