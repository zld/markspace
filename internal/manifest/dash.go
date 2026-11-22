package manifest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"mediasync/internal/clock"
	"mediasync/internal/mederr"
)

// dashSegmentEntry is one explicit SegmentList entry.
type dashSegmentEntry struct {
	N      int64 `xml:"n,attr"`
	T      int64 `xml:"t,attr"`
	D      int64 `xml:"d,attr"`
	Offset int64 `xml:"byteOffset,attr"`
	Length int64 `xml:"byteLength,attr"`
}

// dashMPD models the minimal MPD subset used by the generated fixtures.
type dashMPD struct {
	XMLName xml.Name        `xml:"MPD"`
	Periods []dashPeriod    `xml:"Period"`
}

type dashPeriod struct {
	AdaptationSets []dashAdaptationSet `xml:"AdaptationSet"`
}

type dashTemplate struct {
	Timescale       uint32 `xml:"timescale,attr"`
	Initialization  string `xml:"initialization,attr"`
	Media           string `xml:"media,attr"`
	Duration        int64  `xml:"duration,attr"`
	StartNumber     int    `xml:"startNumber,attr"`
	SegmentTimeline *struct {
		S []struct {
			T int64 `xml:"t,attr"`
			D int64 `xml:"d,attr"`
			R int   `xml:"r,attr"`
		} `xml:"S"`
	} `xml:"SegmentTimeline"`
}

type dashAdaptationSet struct {
	ContentType     string         `xml:"contentType,attr"`
	SegmentTemplate *dashTemplate  `xml:"SegmentTemplate"`
	Representations []struct {
		ID        string `xml:"id,attr"`
		Timescale uint32 `xml:"timescale,attr"`
		Segments  *struct {
			Init  string             `xml:"init,attr"`
			Media string             `xml:"media,attr"`
			Entry []dashSegmentEntry `xml:"Segment"`
		} `xml:"SegmentList"`
	} `xml:"Representation"`
}

// ParseDASH reads an MPD and maps every segment of every representation
// onto the unified nanosecond timeline.
func ParseDASH(r io.Reader) (*Timeline, error) {
	var mpd dashMPD
	if err := xml.NewDecoder(r).Decode(&mpd); err != nil {
		return nil, mederr.New(mederr.CodeInvalidManifest,
			"malformed MPD: "+err.Error())
	}
	out := &Timeline{}
	if len(mpd.Periods) == 0 {
		return nil, mederr.New(mederr.CodeInvalidManifest, "MPD has no Period")
	}
	for _, p := range mpd.Periods {
		for _, as := range p.AdaptationSets {
			tk := TrackVideo
			if strings.EqualFold(as.ContentType, "audio") {
				tk = TrackAudio
			}
			for _, rep := range as.Representations {
				ts := rep.Timescale
				if as.SegmentTemplate != nil && ts == 0 {
					ts = as.SegmentTemplate.Timescale
				}
				if ts == 0 {
					return nil, mederr.New(mederr.CodeZeroTimescale,
						"representation "+rep.ID+" has zero timescale",
						mederr.WithTrack(rep.ID))
				}
				tb, err := clock.NewTimeBase(ts)
				if err != nil {
					return nil, err
				}
				switch {
				case rep.Segments != nil:
					if err := emitSegmentList(out, tb, rep.ID, tk, ts,
						rep.Segments.Init, rep.Segments.Entry); err != nil {
						return nil, err
					}
				case as.SegmentTemplate != nil:
					if err := emitTemplate(out, tb, rep.ID, tk, ts,
						as.SegmentTemplate); err != nil {
						return nil, err
					}
				default:
					return nil, mederr.New(mederr.CodeInvalidManifest,
						"representation "+rep.ID+" declares no segments")
				}
			}
		}
	}
	if len(out.Events) == 0 {
		return nil, mederr.New(mederr.CodeInvalidManifest,
			"MPD produced no timeline events")
	}
	return out, nil
}

// emitSegmentList handles explicit SegmentList entries with byte ranges.
func emitSegmentList(out *Timeline, tb *clock.TimeBase, id string,
	tk TrackKind, ts uint32, init string, entries []dashSegmentEntry) error {
	for _, e := range entries {
		if e.D <= 0 {
			return mederr.New(mederr.CodeInvalidManifest,
				fmt.Sprintf("segment %d has non-positive duration", e.N),
				mederr.WithTrack(id), mederr.WithOffset(e.Offset))
		}
		ev, err := newDASHEvent(tb, id, tk, ts, int(e.N), e.T, e.D,
			fmt.Sprintf("%s/seg-%d.m4s", id, e.N), e.Offset, e.Length)
		if err != nil {
			return err
		}
		if err := out.append(ev); err != nil {
			return err
		}
	}
	return nil
}

// emitTemplate handles SegmentTemplate with either a fixed duration or an
// explicit SegmentTimeline (supporting the @r repeat attribute).
func emitTemplate(out *Timeline, tb *clock.TimeBase, id string,
	tk TrackKind, ts uint32, tpl *dashTemplate) error {
	startNum := tpl.StartNumber
	if startNum == 0 {
		startNum = 1
	}
	if tpl.SegmentTimeline != nil {
		ticks := int64(0)
		num := startNum
		for _, s := range tpl.SegmentTimeline.S {
			if s.T != 0 {
				ticks = s.T
			}
			repeats := s.R + 1
			for k := 0; k < repeats; k++ {
				url := expandTemplate(tpl.Media, id, num, ticks)
				ev, err := newDASHEvent(tb, id, tk, ts, num, ticks, s.D,
					url, -1, 0)
				if err != nil {
					return err
				}
				if err := out.append(ev); err != nil {
					return err
				}
				ticks += s.D
				num++
			}
		}
		return nil
	}
	if tpl.Duration <= 0 {
		return mederr.New(mederr.CodeInvalidManifest,
			"SegmentTemplate without timeline needs a positive duration",
			mederr.WithTrack(id))
	}
	// Fixed-duration templates without an explicit list cannot know the
	// segment count; fixtures always provide SegmentTimeline in that mode,
	// so this branch is guarded as unsupported rather than guessed.
	return mederr.New(mederr.CodeInvalidManifest,
		"fixed-duration SegmentTemplate requires SegmentTimeline",
		mederr.WithTrack(id))
}

// expandTemplate substitutes $RepresentationID$ and $Number$ placeholders.
func expandTemplate(pattern, id string, num int, ticks int64) string {
	s := strings.ReplaceAll(pattern, "$RepresentationID$", id)
	s = strings.ReplaceAll(s, "$Number$", fmt.Sprintf("%d", num))
	return strings.ReplaceAll(s, "$Time$", fmt.Sprintf("%d", ticks))
}

func newDASHEvent(tb *clock.TimeBase, id string, tk TrackKind, ts uint32,
	num int, startTicks, durTicks int64, url string,
	off, length int64) (TimelineEvent, error) {
	start, err := tb.ToNanos(startTicks)
	if err != nil {
		return TimelineEvent{}, err
	}
	end, err := tb.ToNanos(startTicks + durTicks)
	if err != nil {
		return TimelineEvent{}, err
	}
	return TimelineEvent{
		Kind:           KindDASH,
		Representation: id,
		Track:          tk,
		Timescale:      ts,
		Segment: SegmentRef{
			Number: num, URL: url, ByteOffset: off, ByteLength: length,
		},
		StartNS:       start.Ns,
		EndNS:         end.Ns,
		DurationTicks: durTicks,
	}, nil
}

// InitRef describes a DASH initialization segment for one representation.
type InitRef struct {
	Representation string
	URL            string
}

// Inits returns every initialization segment declared by the MPD.
func (t *Timeline) Inits(mpdBytes []byte) ([]InitRef, error) {
	var mpd dashMPD
	if err := xml.Unmarshal(mpdBytes, &mpd); err != nil {
		return nil, mederr.New(mederr.CodeInvalidManifest,
			"malformed MPD while reading inits: "+err.Error())
	}
	var refs []InitRef
	for _, p := range mpd.Periods {
		for _, as := range p.AdaptationSets {
			for _, rep := range as.Representations {
				switch {
				case rep.Segments != nil && rep.Segments.Init != "":
					refs = append(refs, InitRef{rep.ID, rep.Segments.Init})
				case as.SegmentTemplate != nil &&
					as.SegmentTemplate.Initialization != "":
					url := strings.ReplaceAll(
						as.SegmentTemplate.Initialization,
						"$RepresentationID$", rep.ID)
					refs = append(refs, InitRef{rep.ID, url})
				}
			}
		}
	}
	return refs, nil
}

// ParseDASHBytes is a convenience wrapper keeping the raw MPD available for
// InitRef extraction without re-reading the stream.
func ParseDASHBytes(b []byte) (*Timeline, []InitRef, error) {
	tl, err := ParseDASH(bytesReader(b))
	if err != nil {
		return nil, nil, err
	}
	inits, err := tl.Inits(b)
	if err != nil {
		return nil, nil, err
	}
	return tl, inits, nil
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func dupNumberError(rep string, n int) error {
	return mederr.New(mederr.CodeDuplicateNumber,
		fmt.Sprintf("duplicate segment number %d in representation %s", n, rep),
		mederr.WithTrack(rep))
}
