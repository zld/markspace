// Package mpd encodes the reusable manifest model into a static
// MPEG-DASH MPD. Only single-Period, video-only, SegmentTemplate with
// SegmentTimeline manifests are supported; live updates and DRM are out
// of scope. Before anything is written, the encoder independently
// re-sums every SegmentTimeline so a corrupt model can never reach disk.
package mpd

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"dashcheck/internal/model"
)

const onDemandProfile = "urn:mpeg:dash:profile:isoff-on-demand:2011"

type xmlMPD struct {
	XMLName       xml.Name  `xml:"urn:mpeg:dash:schema:mpd:2011 MPD"`
	Type          string    `xml:"type,attr"`
	Profiles      string    `xml:"profiles,attr"`
	MinBufferTime string    `xml:"minBufferTime,attr"`
	MediaDur      string    `xml:"mediaPresentationDuration,attr"`
	Period        xmlPeriod `xml:"Period"`
}

type xmlPeriod struct {
	ID             int             `xml:"id,attr"`
	AdaptationSets []xmlAdaptation `xml:"AdaptationSet"`
}

type xmlAdaptation struct {
	ID              int                 `xml:"id,attr"`
	ContentType     string              `xml:"contentType,attr"`
	MimeType        string              `xml:"mimeType,attr"`
	SegmentAlign    bool                `xml:"segmentAlignment,attr"`
	StartWithSAP    int                 `xml:"startWithSAP,attr"`
	Representations []xmlRepresentation `xml:"Representation"`
}

type xmlRepresentation struct {
	ID        string             `xml:"id,attr"`
	Bandwidth int64              `xml:"bandwidth,attr"`
	Width     int64              `xml:"width,attr"`
	Height    int64              `xml:"height,attr"`
	Template  xmlSegmentTemplate `xml:"SegmentTemplate"`
}

type xmlSegmentTemplate struct {
	Timescale      int64       `xml:"timescale,attr"`
	StartNumber    int64       `xml:"startNumber,attr"`
	Media          string      `xml:"media,attr"`
	Initialization string      `xml:"initialization,attr"`
	Timeline       xmlTimeline `xml:"SegmentTimeline"`
}

type xmlTimeline struct {
	Entries []xmlS `xml:"S"`
}

type xmlS struct {
	T int64 `xml:"t,attr,omitempty"`
	D int64 `xml:"d,attr"`
}

// Marshal renders the manifest to indented MPD bytes after verifying
// every representation's timeline coverage.
func Marshal(m *model.Manifest) ([]byte, error) {
	doc, err := buildDoc(m)
	if err != nil {
		return nil, err
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"MPD XML encoding failed: %v", err)
	}
	return append(append([]byte(xml.Header), body...), '\n'), nil
}

// Write encodes the manifest and writes it to path. The destination
// directory is created when missing; no file is created when encoding
// or coverage verification fails.
func Write(m *model.Manifest, path string) error {
	data, err := Marshal(m)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return model.Errorf(model.CategoryIO, model.CodeWriteFailed,
				"cannot create output directory %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"cannot write MPD %q: %v", path, err)
	}
	return nil
}

func buildDoc(m *model.Manifest) (*xmlMPD, error) {
	if m.Period == nil || len(m.Period.AdaptationSets) != 1 {
		return nil, model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"exactly one Period is required")
	}
	as := m.Period.AdaptationSets[0]
	xmlReps := make([]xmlRepresentation, 0, len(as.Representations))
	for _, rep := range as.Representations {
		entries, err := verifyTimeline(rep)
		if err != nil {
			return nil, err
		}
		xmlReps = append(xmlReps, xmlRepresentation{
			ID: rep.ID, Bandwidth: rep.Bandwidth, Width: rep.Width, Height: rep.Height,
			Template: xmlSegmentTemplate{
				Timescale: rep.Template.Timescale, StartNumber: rep.Template.StartNumber,
				Media: rep.Template.Media, Initialization: rep.Template.Initialization,
				Timeline: xmlTimeline{Entries: entries},
			},
		})
	}
	return &xmlMPD{
		Type: "static", Profiles: onDemandProfile, MinBufferTime: "PT2S",
		MediaDur: ISODuration(m.MediaPresentationDuration),
		Period: xmlPeriod{
			ID: as.ID,
			AdaptationSets: []xmlAdaptation{{
				ID: as.ID, ContentType: as.ContentType, MimeType: as.MimeType,
				SegmentAlign: true, StartWithSAP: 1, Representations: xmlReps,
			}},
		},
	}, nil
}

// verifyTimeline independently re-sums the <S> entries and checks the
// total against the model's recorded coverage.
func verifyTimeline(rep *model.Representation) ([]xmlS, error) {
	var sum int64
	entries := make([]xmlS, 0, len(rep.Template.Timeline))
	for i, e := range rep.Template.Timeline {
		if e.D <= 0 {
			return nil, model.Errorf(model.CategoryIO, model.CodeWriteFailed,
				"representation %q: timeline entry %d has non-positive duration", rep.ID, i)
		}
		var ok bool
		sum, ok = addChecked(sum, e.D)
		if !ok {
			return nil, model.Errorf(model.CategoryIO, model.CodeWriteFailed,
				"representation %q: timeline duration sum overflows int64", rep.ID)
		}
		s := xmlS{D: e.D}
		if e.HasT {
			s.T = e.T
		}
		entries = append(entries, s)
	}
	if sum != rep.Template.CoverageTick {
		return nil, model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"representation %q: re-sumed timeline %d ticks disagrees with model coverage %d",
			rep.ID, sum, rep.Template.CoverageTick)
	}
	return entries, nil
}

// ISODuration formats an exact rational second count as an ISO-8601
// duration. Whole seconds render without a fraction; other values use a
// deterministic 9-digit half-up rounding derived from the rational,
// never a floating-point conversion.
func ISODuration(r model.Rat) string {
	if r.Den() == 1 {
		return fmt.Sprintf("PT%dS", r.Num())
	}
	return "PT" + r.DecimalFixed(9) + "S"
}

func addChecked(a, b int64) (int64, bool) {
	sum := uint64(a) + uint64(b)
	signed := int64(sum)
	if (a >= 0 && b >= 0 && signed < 0) || (a < 0 && b < 0 && signed >= 0) {
		return 0, false
	}
	return signed, true
}
