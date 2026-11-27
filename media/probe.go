package media

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// ColorMeta carries the video colour metadata relevant to delivery
// constraints (nclx triplet plus full-range flag).
type ColorMeta struct {
	Present   bool   `json:"present"`
	Primaries uint16 `json:"primaries,omitempty"`
	Transfer  uint16 `json:"transfer,omitempty"`
	Matrix    uint16 `json:"matrix,omitempty"`
	FullRange bool   `json:"full_range,omitempty"`
}

// SttsEntry is one run-length entry of the decoding time table.
type SttsEntry struct {
	Count uint32 `json:"count"`
	Delta uint32 `json:"delta"`
}

// TrackInfo is the probed description of a single track, with all times
// also expressed on the unified nanosecond base.
type TrackInfo struct {
	TrackID       int          `json:"track_id"`
	Handler       string       `json:"handler"` // "vide" or "soun"
	Timescale     uint32       `json:"timescale"`
	DurationUnits uint64       `json:"duration_units"`
	DurationNanos int64        `json:"duration_nanos"`
	Width         uint16       `json:"width,omitempty"`
	Height        uint16       `json:"height,omitempty"`
	ChannelCount  uint16       `json:"channel_count,omitempty"`
	SampleRate    uint32       `json:"sample_rate,omitempty"`
	Color         ColorMeta    `json:"color"`
	Stts          []SttsEntry  `json:"stts"`
	KeyframeNums  []uint32     `json:"keyframe_nums,omitempty"` // 1-based; nil = all key
	SampleSizes   []uint32     `json:"-"`
	ChunkOffsets  []uint64     `json:"-"`
	SampleCount   uint32       `json:"sample_count"`
}

// IsVideo reports whether the track is a video track.
func (t *TrackInfo) IsVideo() bool { return t.Handler == "vide" }

// Container is the probed description of one MP4/MOV input file.
type Container struct {
	Path       string       `json:"path"`
	FileSHA256 string       `json:"file_sha256"`
	Tracks     []*TrackInfo `json:"tracks"`
}

// VideoTrack returns the first video track, or nil.
func (c *Container) VideoTrack() *TrackInfo {
	for _, t := range c.Tracks {
		if t.IsVideo() {
			return t
		}
	}
	return nil
}

// Probe parses an MP4/MOV file and returns its container description.
// Truncated boxes, out-of-range lengths and illegal timescales are
// reported with their byte offset and track; they never surface as an
// empty track list.
func Probe(path string) (*Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, &Error{Kind: KindInternal, Op: "probe.open", Cause: err}
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, &Error{Kind: KindInternal, Op: "probe.hash", Cause: err}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, &Error{Kind: KindInternal, Op: "probe.seek", Cause: err}
	}

	c := &Container{Path: path, FileSHA256: hex.EncodeToString(h.Sum(nil))}

	// Walk the box tree, remembering where each trak lives so payloads
	// can be extracted with full path context. A read failure is
	// reported with the exact offset parsing stopped at.
	var traks []*mp4.BoxInfo
	var lastType mp4.BoxType
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		lastType = h.BoxInfo.Type
		switch {
		case h.BoxInfo.Type == mp4.BoxTypeMoov(),
			h.BoxInfo.Type == mp4.BoxTypeTrak(),
			h.BoxInfo.Type == mp4.BoxTypeMdia(),
			h.BoxInfo.Type == mp4.BoxTypeMinf(),
			h.BoxInfo.Type == mp4.BoxTypeStbl(),
			h.BoxInfo.Type == mp4.BoxTypeStsd():
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
			if h.BoxInfo.Type == mp4.BoxTypeTrak() {
				bi := h.BoxInfo
				traks = append(traks, &bi)
			}
		}
		return nil, nil
	})
	if err != nil {
		off, _ := f.Seek(0, io.SeekCurrent)
		return nil, &Error{Kind: KindTruncatedBox, Op: "probe.walk",
			Offset: off, HasOffset: true,
			Msg: "box tree unreadable near a '" + lastType.String() + "' box",
			Cause: err}
	}
	if len(traks) == 0 {
		return nil, &Error{Kind: KindTruncatedBox, Op: "probe.walk",
			Msg: "no trak boxes found; refusing to report an empty container"}
	}
	for _, bi := range traks {
		t, err := probeTrack(f, bi)
		if err != nil {
			return nil, err
		}
		c.Tracks = append(c.Tracks, t)
	}
	return c, nil
}
