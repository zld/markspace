package manifest

import (
	"encoding/xml"
	"io"
	"math/big"
	"strconv"
	"strings"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

// Fixture MPD subset:
//
//	<MPD minBufferTime="PT2S"><Period><AdaptationSet contentType="video">
//	  <Representation id bandwidth width height>
//	    <SegmentList timescale duration startNumber>
//	      <SegmentURL media .../>
type mpdDoc struct {
	XMLName       xml.Name `xml:"MPD"`
	MinBufferTime string   `xml:"minBufferTime,attr"`
	Periods       []mpdPer `xml:"Period"`
}

type mpdPer struct {
	ID             string     `xml:"id,attr"`
	AdaptationSets []mpdAdSet `xml:"AdaptationSet"`
}

type mpdAdSet struct {
	ID              string    `xml:"id,attr"`
	ContentType     string    `xml:"contentType,attr"`
	Representations []mpdRepr `xml:"Representation"`
}

type mpdRepr struct {
	ID          string      `xml:"id,attr"`
	Bandwidth   int         `xml:"bandwidth,attr"`
	Width       int         `xml:"width,attr"`
	Height      int         `xml:"height,attr"`
	SegmentList *mpdSegList `xml:"SegmentList"`
}

type mpdSegList struct {
	Timescale   int64       `xml:"timescale,attr"`
	Duration    int64       `xml:"duration,attr"`
	StartNumber uint32      `xml:"startNumber,attr"`
	URLs        []mpdSegURL `xml:"SegmentURL"`
}

type mpdSegURL struct {
	Media string `xml:"media,attr"`
}

// ParseDASH parses a fixture-generated MPD document.
func ParseDASH(r io.Reader) (*Playlist, error) {
	var doc mpdDoc
	dec := xml.NewDecoder(r)
	dec.Strict = true
	if err := dec.Decode(&doc); err != nil {
		return nil, merr.Wrap(err, FormatDASH, "xml", "malformed MPD document")
	}
	if len(doc.Periods) == 0 {
		return nil, merr.New(merr.KindManifest, FormatDASH, "MPD", "MPD has no Period")
	}
	if len(doc.Periods) > 1 {
		return nil, merr.New(merr.KindManifest, FormatDASH, "MPD",
			"fixture subset supports exactly one Period")
	}
	bufSecs, err := parseISODuration(FormatDASH, "MPD:minBufferTime", doc.MinBufferTime)
	if err != nil {
		return nil, err
	}
	if bufSecs <= 0 {
		return nil, merr.New(merr.KindManifest, FormatDASH, "MPD:minBufferTime",
			"minBufferTime must be positive")
	}

	p := &Playlist{Format: FormatDASH}
	// Target duration tick: convert buffer seconds to the unified clock.
	p.TargetDurationTick = big.NewRat(bufSecs, 1).Num().Int64()

	var trackID uint32
	for _, ad := range doc.Periods[0].AdaptationSets {
		kind := media.TrackVideo
		if strings.EqualFold(ad.ContentType, "audio") {
			kind = media.TrackAudio
		}
		for _, repr := range ad.Representations {
			trackID++
			t, err := buildDASHTrack(trackID, kind, repr)
			if err != nil {
				return nil, err
			}
			p.Tracks = append(p.Tracks, t)
		}
	}
	if err := validate(p); err != nil {
		return nil, err
	}
	return p, nil
}

func buildDASHTrack(id uint32, kind media.TrackKind, repr mpdRepr) (*Track, error) {
	loc := "Representation:" + repr.ID
	if repr.SegmentList == nil {
		return nil, merr.New(merr.KindManifest, FormatDASH, loc,
			"Representation has no SegmentList")
	}
	sl := repr.SegmentList
	if sl.Timescale <= 0 {
		return nil, merr.New(merr.KindManifest, FormatDASH, loc,
			"SegmentList missing positive timescale")
	}
	if sl.Duration <= 0 {
		return nil, merr.New(merr.KindManifest, FormatDASH, loc,
			"SegmentList missing positive segment duration")
	}
	if repr.Bandwidth <= 0 {
		return nil, merr.New(merr.KindManifest, FormatDASH, loc,
			"Representation missing positive bandwidth")
	}
	start := sl.StartNumber
	if start == 0 {
		start = 1 // DASH (ISO 23009-1) mandated default
	} else if start < 0 {
		return nil, merr.New(merr.KindManifest, FormatDASH, loc,
			"illegal SegmentList startNumber, must be >= 1")
	}
	t := &Track{
		ID: id, Name: repr.ID, Kind: kind,
		Width: uint16(repr.Width), Height: uint16(repr.Height),
		TargetBitrateBps: repr.Bandwidth,
	}
	durRat := new(big.Rat).SetFrac(big.NewInt(sl.Duration), big.NewInt(sl.Timescale))
	if len(sl.URLs) == 0 {
		return nil, merr.New(merr.KindManifest, FormatDASH, loc,
			"SegmentList declares no SegmentURL entries")
	}
	for i, u := range sl.URLs {
		if strings.TrimSpace(u.Media) == "" {
			return nil, merr.New(merr.KindManifest, FormatDASH,
				loc+":SegmentURL:"+strconv.Itoa(i+1),
				"SegmentURL missing media attribute")
		}
		t.Refs = append(t.Refs, SegmentRef{
			Seq:         start + uint32(i),
			URI:         u.Media,
			DurationNum: durRat.Num().Int64(),
			DurationDen: durRat.Denom().Int64(),
			RawDuration: strconv.FormatInt(sl.Duration, 10) + "/" +
				strconv.FormatInt(sl.Timescale, 10),
		})
	}
	return t, nil
}

// parseISODuration parses the restricted ISO-8601 duration subset used by
// the fixtures: PTnS / PTn.nS (and optionally PTmM).
// Returns seconds as a rational.
func parseISODuration(format, loc, raw string) (int64, error) {
	v := strings.TrimSpace(raw)
	if !strings.HasPrefix(v, "PT") || !strings.HasSuffix(v, "S") {
		return 0, merr.New(merr.KindManifest, format, loc,
			"unsupported ISO-8601 duration, expected PT..S: "+raw)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(v, "PT"), "S")
	parts := strings.Split(body, "M")
	var secs *big.Rat
	switch len(parts) {
	case 1:
		r, ok := new(big.Rat).SetString(strings.TrimSpace(parts[0]))
		if !ok {
			return 0, merr.New(merr.KindManifest, format, loc, "bad duration: "+raw)
		}
		secs = r
	case 2:
		mins, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			return 0, merr.New(merr.KindManifest, format, loc, "bad duration: "+raw)
		}
		r, ok := new(big.Rat).SetString(strings.TrimSpace(parts[1]))
		if !ok {
			return 0, merr.New(merr.KindManifest, format, loc, "bad duration: "+raw)
		}
		secs = new(big.Rat).Add(big.NewRat(mins*60, 1), r)
	default:
		return 0, merr.New(merr.KindManifest, format, loc, "bad duration: "+raw)
	}
	if !secs.IsInt() {
		return 0, merr.New(merr.KindManifest, format, loc,
			"fixture subset requires whole-second durations: "+raw)
	}
	return secs.Num().Int64(), nil
}
