// Package media parses MP4/fMP4 containers, the timed metadata they carry, the
// RTP streams that mirror them, and aligns renditions on one media timeline.
package media

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"mediad/index"
	"mediad/timeline"
)

// ParserVersion is bumped whenever parsing changes shape. It participates in
// index and cache reuse decisions.
const ParserVersion = 1

// Handler is the mdia/hdlr handler type of a track.
type Handler string

const (
	HandlerVideo    Handler = "vide"
	HandlerAudio    Handler = "soun"
	HandlerSubtitle Handler = "subt"
	HandlerText     Handler = "text"
	HandlerMeta     Handler = "meta"
)

// TrackKind classifies a track for the alignment pass.
type TrackKind int

const (
	KindOther TrackKind = iota
	KindVideo
	KindAudio
	KindSubtitle
)

func (k TrackKind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	case KindSubtitle:
		return "subtitle"
	default:
		return "other"
	}
}

// Track is one parsed track with its sample table materialised.
type Track struct {
	ID        uint32        `json:"id"`
	Handler   Handler       `json:"handler"`
	Kind      TrackKind     `json:"kind"`
	TimeScale uint32        `json:"timescale"`
	Base      timeline.Base `json:"-"`
	// Width, Height are video display dimensions in pixels.
	Width, Height int
	// Language is the ISO-639-2/T code when present.
	Language string
	// SampleEntry is the codec fourcc (avc1, mp4a, tx3g, ...).
	SampleEntry string
	// CodecPrivate is the codec configuration box payload (avcC, esds, ...).
	CodecPrivate []byte
	// Samples is ordered by decode time.
	Samples []index.SampleIndex
	// sourcePath is the file the samples live in.
	sourcePath string
}

// TimeBase returns the track time base with 32-bit wrap semantics, matching
// how raw sample decode times are stored.
func (t Track) TimeBase() timeline.Base {
	return timeline.Base{Name: fmt.Sprintf("track-%d", t.ID), TimeScale: t.TimeScale}
}

// Keyframes returns the indices of sync samples, ascending.
func (t Track) Keyframes() []int {
	var out []int
	for i, s := range t.Samples {
		if s.Sync {
			out = append(out, i)
		}
	}
	return out
}

// EndTicks returns the decode end of the last sample.
func (t Track) EndTicks() int64 {
	if len(t.Samples) == 0 {
		return 0
	}
	last := t.Samples[len(t.Samples)-1]
	return last.DecodeTicks + last.DurationTicks
}

// Clip is a parsed MP4 file: its tracks plus the box layout used for
// fingerprinting.
type Clip struct {
	Path   string
	Tracks []Track
	// Regions classifies the file's bytes for the three-way fingerprint.
	Regions []index.Region
	// Issues records non-fatal structural findings (for example a track with
	// no sample table); fatal problems are returned as errors.
	Issues timeline.Issues
}

// VideoTrack returns the first video track.
func (c *Clip) VideoTrack() (Track, bool) {
	for _, t := range c.Tracks {
		if t.Kind == KindVideo {
			return t, true
		}
	}
	return Track{}, false
}

// Track returns the track with id.
func (c *Clip) Track(id uint32) (Track, bool) {
	for _, t := range c.Tracks {
		if t.ID == id {
			return t, true
		}
	}
	return Track{}, false
}

// ParseFile reads and parses path.
func ParseFile(path string) (*Clip, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c, err := Parse(path, data)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Parse parses an MP4 held in memory. Every structural failure is reported with
// the file name and a byte offset so damaged input can be located exactly.
func Parse(path string, data []byte) (*Clip, error) {
	clip := &Clip{Path: path}
	r := bytes.NewReader(data)

	top, err := topLevelBoxes(r)
	if err != nil {
		return nil, locate(path, 0, err)
	}

	moov, ok := findBox(top, "moov")
	if !ok {
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError,
			Code:     timeline.CodeUnsupported,
			File:     path,
			Message:  "no moov box: cannot build a sample index",
		}}
	}

	// Footprint of the box tree, for the container digest.
	for _, b := range top {
		switch string(b.typ[:]) {
		case "mdat":
			continue
		case "udta", "meta", "emsg":
			clip.Regions = append(clip.Regions, index.Region{Offset: int64(b.offset + b.headerLen), Size: int64(b.size - b.headerLen), Class: index.ClassMetadata})
		default:
			clip.Regions = append(clip.Regions, index.Region{Offset: int64(b.offset + b.headerLen), Size: int64(b.size - b.headerLen), Class: index.ClassContainer})
		}
	}

	trakBoxes, err := childBoxes(r, moov, "trak")
	if err != nil {
		return nil, locate(path, int64(moov.offset), err)
	}
	if len(trakBoxes) == 0 {
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeUnsupported, File: path,
			ByteOffset: int64(moov.offset), Message: "moov contains no tracks",
		}}
	}
	for _, trak := range trakBoxes {
		t, terr := parseTrack(r, path, trak)
		if terr != nil {
			return nil, terr
		}
		clip.Tracks = append(clip.Tracks, t)
	}
	sort.Slice(clip.Tracks, func(i, j int) bool { return clip.Tracks[i].ID < clip.Tracks[j].ID })
	return clip, nil
}

// boxSpan is the location and extent of one box.
func parseTrack(r *bytes.Reader, path string, trak boxSpan) (Track, error) {
	var t Track
	t.sourcePath = path

	tkhd, ok, err := readChildPayload(r, trak, "tkhd")
	if err != nil {
		return t, locate(path, int64(trak.offset), err)
	}
	if ok {
		var box mp4.Tkhd
		if _, err := mp4.Unmarshal(bytes.NewReader(tkhd), uint64(len(tkhd)), &box, mp4.Context{}); err != nil {
			return t, locate(path, int64(trak.offset), fmt.Errorf("tkhd: %w", err))
		}
		t.ID = box.TrackID
		t.Width = int(box.Width >> 16)
		t.Height = int(box.Height >> 16)
	} else {
		return t, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeUnsupported,
			File: path, ByteOffset: int64(trak.offset), Message: "trak without tkhd",
		}}
	}

	mdia, ok := findBox(mustChildren(r, trak), "mdia")
	if !ok {
		return t, locate(path, int64(trak.offset), errors.New("trak without mdia"))
	}

	mdhdPayload, ok, err := readChildPayload(r, mdia, "mdhd")
	if err != nil {
		return t, locate(path, int64(mdia.offset), err)
	}
	if !ok {
		return t, locate(path, int64(mdia.offset), errors.New("mdia without mdhd"))
	}
	var mdhd mp4.Mdhd
	if _, err := mp4.Unmarshal(bytes.NewReader(mdhdPayload), uint64(len(mdhdPayload)), &mdhd, mp4.Context{}); err != nil {
		return t, locate(path, int64(mdia.offset), fmt.Errorf("mdhd: %w", err))
	}
	t.TimeScale = mdhd.Timescale
	if t.TimeScale == 0 {
		return t, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeInvalidTime,
			File: path, TrackID: t.ID, ByteOffset: int64(mdia.offset), Message: "mdhd timescale is zero",
		}}
	}
	t.Base = timeline.Base{Name: fmt.Sprintf("track-%d", t.ID), TimeScale: t.TimeScale}
	lang := mdhd.Language
	if lang[0] > 0 {
		t.Language = string(lang[:])
	}

	hdlrPayload, ok, err := readChildPayload(r, mdia, "hdlr")
	if err != nil {
		return t, locate(path, int64(mdia.offset), err)
	}
	if !ok {
		return t, locate(path, int64(mdia.offset), errors.New("mdia without hdlr"))
	}
	var hdlr mp4.Hdlr
	if _, err := mp4.Unmarshal(bytes.NewReader(hdlrPayload), uint64(len(hdlrPayload)), &hdlr, mp4.Context{}); err != nil {
		return t, locate(path, int64(mdia.offset), fmt.Errorf("hdlr: %w", err))
	}
	t.Handler = Handler(hdlr.HandlerType[:])
	t.Kind = kindFor(t.Handler)

	mdiaChildren, err := childBoxes(r, mdia, "minf")
	if err != nil {
		return t, locate(path, int64(mdia.offset), err)
	}
	if len(mdiaChildren) == 0 {
		return t, locate(path, int64(mdia.offset), errors.New("mdia without minf"))
	}
	minf := mdiaChildren[0]
	stblBoxes, err := childBoxes(r, minf, "stbl")
	if err != nil {
		return t, locate(path, int64(minf.offset), err)
	}
	if len(stblBoxes) == 0 {
		return t, locate(path, int64(minf.offset), errors.New("minf without stbl"))
	}
	if err := parseSampleTable(r, path, stblBoxes[0], &t); err != nil {
		return t, err
	}
	return t, nil
}
