package mediacore

import (
	"errors"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// TrackKind classifies a track by its media handler.
type TrackKind string

const (
	KindVideoTrack TrackKind = "vide"
	KindAudioTrack TrackKind = "soun"
	KindOtherTrack TrackKind = "other"
)

// BoxRef points at a byte range in the source file. For mdat refs the range
// covers the payload only; for container boxes it covers header and payload.
type BoxRef struct {
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
}

// End returns the first byte after the range.
func (r BoxRef) End() int64 { return r.Offset + r.Size }

// Contains reports whether [start,start+n) lies inside the range.
func (r BoxRef) Contains(start, n int64) bool {
	if n < 0 || start < r.Offset {
		return false
	}
	return start-r.Offset <= r.Size-n
}

// SampleInfo is container metadata for one media sample. Payload bytes are
// never held here; Offset points into the source file.
type SampleInfo struct {
	Index    int    `json:"index"`
	Offset   int64  `json:"offset"`
	Size     int64  `json:"size"`
	DTS      int64  `json:"dts"`
	Duration uint32 `json:"duration"`
	CTS      int64  `json:"cts"`
	Sync     bool   `json:"sync"`
}

// Track is one demuxed track: metadata, timebase and the sample table.
type Track struct {
	ID              uint32       `json:"id"`
	Kind            TrackKind    `json:"kind"`
	HandlerType     string       `json:"handler_type"`
	HandlerName     string       `json:"handler_name"`
	Codec           string       `json:"codec"`
	Timescale       uint32       `json:"timescale"`
	DurationTicks   int64        `json:"duration_ticks"`
	StartTicks      int64        `json:"start_ticks"`
	EndTicks        int64        `json:"end_ticks"`
	Unknown         bool         `json:"unknown"`
	EditOffsetTicks int64        `json:"edit_offset_ticks"`
	Width           uint16       `json:"width,omitempty"`
	Height          uint16       `json:"height,omitempty"`
	Channels        uint16       `json:"channels,omitempty"`
	SampleEntry     BoxRef       `json:"sample_entry"`
	Samples         []SampleInfo `json:"-"`
}

// SampleCount returns the number of samples in the track.
func (t *Track) SampleCount() int { return len(t.Samples) }

// Timebase returns the track clock.
func (t *Track) Timebase() Timebase { return NewTimebase(t.Timescale) }

// Container is the result of demuxing one MP4/MOV file.
type Container struct {
	Path           string   `json:"path"`
	Size           int64    `json:"size"`
	Brand          string   `json:"brand"`
	MovieTimescale uint32   `json:"movie_timescale"`
	MovieDuration  int64    `json:"movie_duration"`
	FastStart      bool     `json:"fast_start"`
	Tracks         []Track  `json:"tracks"`
	Ftyp           BoxRef   `json:"ftyp"`
	Moov           BoxRef   `json:"moov"`
	Mdats          []BoxRef `json:"mdats"`
}

// DemuxOptions tunes demuxer tolerance.
type DemuxOptions struct {
	// AllowUnknownTracks keeps tracks whose handler is neither video nor
	// audio, including their raw sample bytes. When false, such a track is a
	// hard error so callers can refuse to pass opaque data through.
	AllowUnknownTracks bool
	// ValidateOffsets rejects sample byte ranges outside any mdat payload.
	ValidateOffsets bool
}

// DefaultDemuxOptions is the strict, preserving configuration.
func DefaultDemuxOptions() DemuxOptions {
	return DemuxOptions{AllowUnknownTracks: true, ValidateOffsets: true}
}

// TrackByID returns a pointer to the track with the given ID.
func (c *Container) TrackByID(id uint32) *Track {
	for i := range c.Tracks {
		if c.Tracks[i].ID == id {
			return &c.Tracks[i]
		}
	}
	return nil
}

// DemuxFile parses container metadata for path. Media payload is not read
// into memory: only the moov sample tables are parsed, and each sample keeps
// its absolute byte offset in the file.
func DemuxFile(path string, opts DemuxOptions) (*Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, wrapErr(KindIO, "demux", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, wrapErr(KindIO, "demux", err)
	}
	c := &Container{Path: path, Size: st.Size()}
	if err := scanTopLevel(f, c); err != nil {
		return nil, err
	}
	if c.Moov.Size == 0 {
		return nil, fail(KindContainer, "demux", "no moov box in %s", path)
	}
	if err := readBrand(f, c); err != nil {
		return nil, err
	}
	if err := parseMovieHeader(f, c); err != nil {
		return nil, err
	}
	if err := parseTracks(f, c, opts); err != nil {
		return nil, err
	}
	if len(c.Tracks) == 0 {
		return nil, fail(KindContainer, "demux", "moov contains no tracks")
	}
	return c, nil
}

// scanTopLevel walks the top-level boxes and records the ones remux needs.
// It rejects truncated containers before go-mp4 can silently treat a short
// moov as an extended box.
func scanTopLevel(f io.ReadSeeker, c *Container) error {
	for {
		bi, err := mp4.ReadBoxInfo(f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return wrapErr(KindContainer, "demux", fmt.Errorf("invalid top-level box: %w", err))
		}
		if bi.Size < bi.HeaderSize {
			return fail(KindContainer, "demux", "box %s at %d declares size %d smaller than header %d", bi.Type, bi.Offset, bi.Size, bi.HeaderSize)
		}
		end := int64(bi.Offset + bi.Size)
		if end > c.Size && !bi.ExtendToEOF {
			return fail(KindContainer, "demux", "truncated container: box %s at %d ends at %d but file is %d bytes", bi.Type, bi.Offset, end, c.Size)
		}
		switch bi.Type {
		case mp4.BoxTypeFtyp():
			if c.Ftyp.Size == 0 {
				c.Ftyp = BoxRef{Offset: int64(bi.Offset), Size: int64(bi.Size)}
			}
		case mp4.BoxTypeMoov():
			if c.Moov.Size != 0 {
				return fail(KindContainer, "demux", "multiple moov boxes are not supported")
			}
			c.Moov = BoxRef{Offset: int64(bi.Offset), Size: int64(bi.Size)}
		case mp4.BoxTypeMdat():
			c.Mdats = append(c.Mdats, BoxRef{
				Offset: int64(bi.Offset + bi.HeaderSize),
				Size:   int64(bi.Size - bi.HeaderSize),
			})
		case mp4.BoxTypeMoof(), mp4.BoxTypeMfra():
			return fail(KindContainer, "demux", "fragmented input (%s) is not supported", bi.Type)
		}
		if _, err := f.Seek(end, io.SeekStart); err != nil {
			return wrapErr(KindIO, "demux", err)
		}
	}
	if len(c.Mdats) > 0 && c.Moov.Size != 0 {
		c.FastStart = c.Moov.Offset < c.Mdats[0].Offset
	}
	return nil
}

// boxInfoAt re-reads the header of the box starting at offset, including
// extended-size headers, so nested extraction sees a correct BoxInfo.
func boxInfoAt(f io.ReadSeeker, ref BoxRef) (*mp4.BoxInfo, error) {
	if _, err := f.Seek(ref.Offset, io.SeekStart); err != nil {
		return nil, wrapErr(KindIO, "demux", err)
	}
	bi, err := mp4.ReadBoxInfo(f)
	if err != nil {
		return nil, wrapErr(KindContainer, "demux", err)
	}
	if bi.Offset != uint64(ref.Offset) || bi.Size != uint64(ref.Size) {
		return nil, fail(KindContainer, "demux", "box header mismatch at %d: %s size %d (expected %d)", ref.Offset, bi.Type, bi.Size, ref.Size)
	}
	return bi, nil
}

func readBrand(f io.ReadSeeker, c *Container) error {
	if c.Ftyp.Size == 0 {
		return nil
	}
	bi, err := boxInfoAt(f, c.Ftyp)
	if err != nil {
		return err
	}
	if bi.Size-bi.HeaderSize < 8 {
		return fail(KindContainer, "demux", "ftyp box too small at %d", c.Ftyp.Offset)
	}
	payload := make([]byte, 8)
	if _, err := f.Seek(int64(bi.Offset+bi.HeaderSize), io.SeekStart); err != nil {
		return wrapErr(KindIO, "demux", err)
	}
	if _, err := io.ReadFull(f, payload); err != nil {
		return wrapErr(KindContainer, "demux", fmt.Errorf("reading ftyp brand: %w", err))
	}
	c.Brand = string(payload[:4])
	return nil
}

func parseMovieHeader(f io.ReadSeeker, c *Container) error {
	moov, err := boxInfoAt(f, c.Moov)
	if err != nil {
		return err
	}
	boxes, err := mp4.ExtractBoxesWithPayload(f, moov, []mp4.BoxPath{{mp4.BoxTypeMvhd()}})
	if err != nil {
		return wrapErr(KindContainer, "demux", err)
	}
	if len(boxes) != 1 {
		return fail(KindContainer, "demux", "moov must contain exactly one mvhd box")
	}
	mvhd, ok := boxes[0].Payload.(*mp4.Mvhd)
	if !ok {
		return fail(KindContainer, "demux", "unexpected mvhd payload type")
	}
	if mvhd.Timescale == 0 {
		return fail(KindContainer, "demux", "mvhd timescale is zero")
	}
	c.MovieTimescale = mvhd.Timescale
	c.MovieDuration = int64(mvhd.GetDuration())
	return nil
}

func parseTracks(f io.ReadSeeker, c *Container, opts DemuxOptions) error {
	moov, err := boxInfoAt(f, c.Moov)
	if err != nil {
		return err
	}
	bis, err := mp4.ExtractBoxes(f, moov, []mp4.BoxPath{{mp4.BoxTypeTrak()}})
	if err != nil {
		return wrapErr(KindContainer, "demux", err)
	}
	if len(bis) == 0 {
		return fail(KindContainer, "demux", "moov has no trak boxes")
	}
	ids := map[uint32]bool{}
	for _, bi := range bis {
		t, err := parseTrack(f, bi, c.Mdats, opts)
		if err != nil {
			return err
		}
		if ids[t.ID] {
			return fail(KindContainer, "demux", "duplicate track ID %d", t.ID)
		}
		ids[t.ID] = true
		c.Tracks = append(c.Tracks, *t)
	}
	return nil
}
