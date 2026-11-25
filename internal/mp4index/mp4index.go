// Package mp4index reads MP4 container elements and time-base metadata with
// go-mp4 so an MP4 index sidecar shares the media model with Matroska masters.
// It does not decode samples: it records element offsets, track timescales and
// sample timing tables, then maps every instant onto the shared nanosecond base.
package mp4index

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

// Limits bound a hostile file. go-mp4 expands only the containers we ask for.
const maxDepth = 12

// Parse reads an MP4 sidecar and returns a media asset. Track sample times are
// drawn from the stts table so cross-track deviation is comparable with the
// Matroska path.
func Parse(path string) (*media.Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1, "open %s", path)
	}
	defer f.Close()
	hash, size, err := media.HashFile(f)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1, "seek")
	}

	a := &media.Asset{Path: path, Size: size, SHA256: hash, Container: media.ContainerMP4,
		SequenceBase: -1}
	a.Clock = timeline.MustClock(1_000_000_000)

	moovTimescale := uint64(0)
	trackTimescales := map[uint32]uint64{}
	var pendingTrack uint32
	// Sample tables accumulate per track; stts entries are ordered.
	type sttsTable struct {
		timescale uint64
		entries   []mp4.SttsEntry
		handler   string
	}
	tables := map[uint32]*sttsTable{}
	var trackOrder []uint32
	depths := 0

	var walkErr error
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		if walkErr != nil {
			return nil, walkErr
		}
		a.Elements = append(a.Elements, media.Element{
			ID:     boxID(h.BoxInfo.Type),
			Name:   h.BoxInfo.Type.String(),
			Offset: int64(h.BoxInfo.Offset),
			Size:   h.BoxInfo.Size,
		})
		if len(h.Path) > maxDepth {
			walkErr = timeline.New(timeline.StageParse, timeline.CatLimitExceeded,
				boxID(h.BoxInfo.Type), int64(h.BoxInfo.Offset),
				"mp4 box nesting exceeds limit %d", maxDepth)
			return nil, walkErr
		}
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeEdts(), mp4.BoxTypeDinf(), mp4.BoxTypeMvex():
			depths++
			return h.Expand()
		case mp4.BoxTypeMvhd():
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapBox(err, h)
			}
			moovTimescale = uint64(b.(*mp4.Mvhd).Timescale)
		case mp4.BoxTypeTkhd():
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapBox(err, h)
			}
			pendingTrack = b.(*mp4.Tkhd).TrackID
			if _, ok := tables[pendingTrack]; !ok {
				tables[pendingTrack] = &sttsTable{}
				trackOrder = append(trackOrder, pendingTrack)
			}
		case mp4.BoxTypeHdlr():
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapBox(err, h)
			}
			t := tables[pendingTrack]
			if t != nil {
				t.handler = string(bytes.TrimRight(b.(*mp4.Hdlr).HandlerType[:], "\x00"))
			}
		case mp4.BoxTypeMdhd():
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapBox(err, h)
			}
			md := b.(*mp4.Mdhd)
			ts := uint64(md.Timescale)
			trackTimescales[pendingTrack] = ts
			if t := tables[pendingTrack]; t != nil {
				t.timescale = ts
			}
		case mp4.BoxTypeStts():
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapBox(err, h)
			}
			if t := tables[pendingTrack]; t != nil {
				t.entries = append(t.entries, b.(*mp4.Stts).Entries...)
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if walkErr != nil {
		return nil, walkErr
	}
	if moovTimescale == 0 {
		return nil, timeline.New(timeline.StageParse, timeline.CatMissingElement, boxID(mp4.BoxTypeMvhd()),
			0, "moov has no usable mvhd timescale")
	}
	a.TimecodeScale = 1
	a.Version = 1
	a.DocType = "isom"

	order := 0
	for _, tn := range trackOrder {
		t := tables[tn]
		ts := t.timescale
		if ts == 0 {
			return nil, timeline.New(timeline.StageParse, timeline.CatMissingElement,
				boxID(mp4.BoxTypeMdhd()), 0, "track %d has no mdhd timescale", tn)
		}
		clock, err := timeline.NewClock(ts)
		if err != nil {
			return nil, err
		}
		typ := trackTypeForHandler(t.handler)
		tr := tracks.Track{Number: uint64(tn), UID: uint64(tn), Type: typ,
			CodecID: "mp4/" + t.handler, Language: "und"}
		a.Tracks = append(a.Tracks, tr)

		var sample uint64
		for _, e := range t.entries {
			for i := uint32(0); i < e.SampleCount; i++ {
				start := clock.ToNanos(sample)
				dur := clock.ToNanos(uint64(e.SampleDelta))
				off := int64(sample) // table index, not a byte offset
				a.Fragments = append(a.Fragments, media.Fragment{
					TrackNumber: uint64(tn),
					Order:       order,
					Seq:         -1,
					Start:       start,
					Duration:    dur,
					Reference:   0,
					Cluster:     0,
					Offset:      off,
					Keyframe:    true,
					Kind:        "stts",
				})
				order++
				sample += uint64(e.SampleDelta)
				if len(a.Fragments) > 1<<22 {
					return nil, timeline.New(timeline.StageParse, timeline.CatLimitExceeded,
						boxID(mp4.BoxTypeStts()), 0, "sample table exceeds 4M entries")
				}
			}
		}
	}
	a.Duration = durationFromTracks(a.Fragments)
	tracks.SortTracks(a.Tracks)
	return a, nil
}

func wrapBox(err error, h *mp4.ReadHandle) error {
	return timeline.Wrap(err, timeline.StageParse, timeline.CatTruncated,
		boxID(h.BoxInfo.Type), int64(h.BoxInfo.Offset), "read %s payload", h.BoxInfo.Type.String())
}

// boxID packs a four-character MP4 box type into the same uint32 element ID
// space the Matroska side uses, so reports can print one field for both.
func boxID(t mp4.BoxType) uint32 {
	return uint32(t[0])<<24 | uint32(t[1])<<16 | uint32(t[2])<<8 | uint32(t[3])
}

func trackTypeForHandler(name string) tracks.TrackType {
	switch name {
	case "vide":
		return tracks.TypeVideo
	case "soun":
		return tracks.TypeAudio
	case "text", "sbtl", "subt", "clcp":
		return tracks.TypeSubtitle
	default:
		return tracks.TypeMetadata
	}
}

func durationFromTracks(fs []media.Fragment) timeline.Nanosecond {
	var max timeline.Nanosecond
	for _, f := range fs {
		if end := f.End(); end > max {
			max = end
		}
	}
	return max
}

// ErrNoMoov is returned when a sidecar has no moov box.
var ErrNoMoov = errors.New("mp4: moov box missing")

// Describe renders a one-line summary.
func Describe(a *media.Asset) string {
	return fmt.Sprintf("%s: %d elements, %d tracks, %d samples",
		a.Path, a.ElementCount(), len(a.Tracks), len(a.Fragments))
}
