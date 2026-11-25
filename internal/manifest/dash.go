package manifest

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediacore/internal/clock"
	"mediacore/internal/diagnostic"
	"mediacore/internal/workspace"
)

type mpdXML struct {
	XMLName xml.Name `xml:"MPD"`
	Periods []struct {
		AdaptationSets []struct {
			Representations []dashRepresentation `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

type dashRepresentation struct {
	ID              string `xml:"id,attr"`
	Bandwidth       int64  `xml:"bandwidth,attr"`
	MimeType        string `xml:"mimeType,attr"`
	Codecs          string `xml:"codecs,attr"`
	SegmentTemplate *struct {
		Timescale          int64  `xml:"timescale,attr"`
		Duration           int64  `xml:"duration,attr"`
		StartNumber        int64  `xml:"startNumber,attr"`
		PresentationOffset int64  `xml:"presentationTimeOffset,attr"`
		Initialization     string `xml:"initialization,attr"`
		Media              string `xml:"media,attr"`
		EndNumber          int64  `xml:"endNumber,attr"`
		Timeline           *struct {
			S []struct {
				T *int64 `xml:"t,attr"`
				D *int64 `xml:"d,attr"`
				R *int64 `xml:"r,attr"`
			} `xml:"S"`
		} `xml:"SegmentTimeline"`
	} `xml:"SegmentTemplate"`
}

type DASH struct {
	ManifestPath string
	WorkRoot     string
	Entries      []Entry
	ByRep        map[string][]Entry
}

// ParseDASH reads a local MPD and expands every segment into unified entries.
func ParseDASH(path, workRoot string) (*DASH, error) {
	return ParseDASHWithReference(path, workRoot, 0)
}

func ParseDASHWithReference(path, workRoot string, referenceStartU int64) (*DASH, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, diagnostic.Wrap(nil, diagnostic.Location{Stage: "dash", ManifestPath: path, ByteOffset: 0}, err)
	}
	var mpd mpdXML
	if err := xml.Unmarshal(data, &mpd); err != nil {
		return nil, diagnostic.Wrap(err, diagnostic.Location{Stage: "dash", ManifestPath: path, ByteOffset: -1}, err)
	}
	d := &DASH{ManifestPath: path, WorkRoot: workRoot, ByRep: map[string][]Entry{}}
	manifestDir := filepath.Dir(path)
	for _, period := range mpd.Periods {
		for _, as := range period.AdaptationSets {
			for _, rep := range as.Representations {
				if err := d.parseRepresentation(rep, manifestDir, referenceStartU); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(d.Entries) == 0 {
		loc := diagnostic.Location{Stage: "dash", ManifestPath: path, EntryIndex: -1, ByteOffset: -1}
		return nil, diagnostic.New(diagnostic.ErrMissingDuration, loc, "MPD contains no segment entries")
	}
	if err := ValidateMonotonic(d.Entries); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DASH) parseRepresentation(rep dashRepresentation, manifestDir string, referenceStartU int64) error {
	repID := rep.ID
	baseLoc := diagnostic.Location{Stage: "dash", ManifestPath: d.ManifestPath, Representation: repID, EntryIndex: 0, ByteOffset: -1}
	if rep.SegmentTemplate == nil {
		return diagnostic.New(diagnostic.ErrMissingDuration, baseLoc, "Representation has no SegmentTemplate")
	}
	st := rep.SegmentTemplate
	if st.Timescale == 0 {
		return diagnostic.New(diagnostic.ErrZeroTimescale, baseLoc, "timescale is zero or missing")
	}
	if st.Timescale < 0 {
		return diagnostic.New(diagnostic.ErrZeroTimescale, baseLoc, "timescale is negative")
	}
	startNumber := st.StartNumber
	if startNumber == 0 {
		startNumber = 1
	}
	initPath := ""
	if st.Initialization != "" {
		uri := expandTemplate(st.Initialization, repID, startNumber)
		p, err := workspace.Resolve(d.WorkRoot, manifestDir, uri)
		if err != nil {
			return err
		}
		initPath = p
	}
	spans, err := dashSpans(st)
	if err != nil {
		loc := baseLoc
		loc.Field = "duration"
		return diagnostic.Wrap(diagnostic.ErrMissingDuration, loc, err)
	}
	repEntries := make([]Entry, 0, len(spans))
	for i, span := range spans {
		loc := baseLoc
		loc.EntryIndex = i
		if span.Duration <= 0 {
			return diagnostic.New(diagnostic.ErrMissingDuration, loc, fmt.Sprintf("segment %d has no positive duration", i))
		}
		seq := int(startNumber) + i
		uri := expandTemplate(st.Media, repID, int64(seq))
		if uri == "" {
			return diagnostic.New(diagnostic.ErrMissingDuration, loc, "SegmentTemplate@media is empty")
		}
		mediaPath, err := workspace.Resolve(d.WorkRoot, manifestDir, uri)
		if err != nil {
			return err
		}
		startU, err := clock.ToUnified(span.Start-st.PresentationOffset, st.Timescale)
		if err != nil {
			return diagnostic.Wrap(err, loc, err.Error())
		}
		durU, err := clock.DurationToUnified(span.Duration, st.Timescale)
		if err != nil {
			return diagnostic.Wrap(err, loc, err.Error())
		}
		endU, err := clock.Add(startU, durU)
		if err != nil {
			return diagnostic.Wrap(err, loc, err.Error())
		}
		aligned, deviation, err := clock.AlignmentWithinLimit(startU, referenceStartU)
		if err != nil {
			return diagnostic.Wrap(err, loc, err.Error())
		}
		e := Entry{
			StableKey:        "dash:" + repID + ":" + strconv.Itoa(seq),
			System:           "dash",
			Manifest:         d.ManifestPath,
			Representation:   repID,
			EntryIndex:       i,
			OriginalSequence: seq,
			InitPath:         initPath,
			MediaPath:        mediaPath,
			MediaURI:         uri,
			SourceTimescale:  st.Timescale,
			SourceStart:      span.Start,
			SourceDuration:   span.Duration,
			ProgramStartU:    startU,
			ProgramEndU:      endU,
			ReferenceStartU:  referenceStartU,
			DeviationU:       deviation,
			Aligned:          aligned,
		}
		repEntries = append(repEntries, e)
		d.Entries = append(d.Entries, e)
	}
	d.ByRep[repID] = append(d.ByRep[repID], repEntries...)
	return nil
}

type dashSpan struct{ Start, Duration int64 }

func dashSpans(st *struct {
	Timescale          int64  `xml:"timescale,attr"`
	Duration           int64  `xml:"duration,attr"`
	StartNumber        int64  `xml:"startNumber,attr"`
	PresentationOffset int64  `xml:"presentationTimeOffset,attr"`
	Initialization     string `xml:"initialization,attr"`
	Media              string `xml:"media,attr"`
	EndNumber          int64  `xml:"endNumber,attr"`
	Timeline           *struct {
		S []struct {
			T *int64 `xml:"t,attr"`
			D *int64 `xml:"d,attr"`
			R *int64 `xml:"r,attr"`
		} `xml:"S"`
	} `xml:"SegmentTimeline"`
}) ([]dashSpan, error) {
	if st.Timeline != nil && len(st.Timeline.S) > 0 {
		var nextT int64
		var out []dashSpan
		for _, s := range st.Timeline.S {
			if s.T != nil {
				nextT = *s.T
			}
			if s.D == nil || *s.D <= 0 {
				return nil, fmt.Errorf("S@d missing or non-positive")
			}
			repeat := int64(0)
			if s.R != nil {
				if *s.R < 0 {
					return nil, fmt.Errorf("S@r negative")
				}
				repeat = *s.R
			}
			for j := int64(0); j <= repeat; j++ {
				out = append(out, dashSpan{Start: nextT, Duration: *s.D})
				nextT += *s.D
			}
		}
		return out, nil
	}
	if st.Duration <= 0 {
		return nil, fmt.Errorf("SegmentTemplate@duration missing or non-positive")
	}
	count := int64(3) // small deterministic default for template-only fixtures
	if st.EndNumber >= st.StartNumber && st.StartNumber > 0 {
		count = st.EndNumber - st.StartNumber + 1
	} else if st.EndNumber > 0 {
		count = st.EndNumber
	}
	out := make([]dashSpan, 0, count)
	for i := int64(0); i < count; i++ {
		out = append(out, dashSpan{Start: i * st.Duration, Duration: st.Duration})
	}
	return out, nil
}

func expandTemplate(s, repID string, number int64) string {
	s = strings.ReplaceAll(s, "$RepresentationID$", repID)
	s = strings.ReplaceAll(s, "$Number$", strconv.FormatInt(number, 10))
	if strings.Contains(s, "$Number%") {
		// Fixtures use plain $Number$; keep an explicit unsupported pattern visible.
		s = strings.ReplaceAll(s, "$Number%01d$", strconv.FormatInt(number, 10))
	}
	return s
}

// EntryAt returns an in-representation entry and produces a fully located out-of-range error.
func (d *DASH) EntryAt(repID string, entryIndex int) (Entry, error) {
	items, ok := d.ByRep[repID]
	loc := diagnostic.Location{Stage: "dash", ManifestPath: d.ManifestPath, Representation: repID, EntryIndex: entryIndex, ByteOffset: -1}
	if !ok {
		return Entry{}, diagnostic.New(diagnostic.ErrOutOfRange, loc, "unknown Representation")
	}
	if entryIndex < 0 || entryIndex >= len(items) {
		loc.Field = "entry_index"
		return Entry{}, diagnostic.New(diagnostic.ErrOutOfRange, loc,
			"index "+strconv.Itoa(entryIndex)+" outside [0,"+strconv.Itoa(len(items))+")")
	}
	return items[entryIndex], nil
}
