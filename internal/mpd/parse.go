package mpd

import (
	"bytes"
	"encoding/xml"
	"fmt"

	"dashgen/internal/manifest"
)

// Parse decodes an MPD document previously produced by Marshal and applies
// structural sanity checks (periods, adaptation sets, representations and
// segment templates must all be present and well-formed).
func Parse(data []byte) (*manifest.MPD, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var doc manifest.MPD
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("mpd: parse: %w", err)
	}
	if err := sanityCheck(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func sanityCheck(doc *manifest.MPD) error {
	if len(doc.Periods) == 0 {
		return fmt.Errorf("mpd: parse: document has no Period")
	}
	for pi, period := range doc.Periods {
		if len(period.AdaptationSets) == 0 {
			return fmt.Errorf("mpd: parse: period %s has no AdaptationSet", period.ID)
		}
		for ai, set := range period.AdaptationSets {
			if len(set.Representations) == 0 {
				return fmt.Errorf("mpd: parse: period %d adaptation set %d has no Representation", pi, ai)
			}
			for _, rep := range set.Representations {
				if rep.ID == "" {
					return fmt.Errorf("mpd: parse: representation with empty id in set %d", set.ID)
				}
				if rep.SegmentTemplate == nil {
					return fmt.Errorf("mpd: parse: representation %q has no SegmentTemplate", rep.ID)
				}
				if rep.SegmentTemplate.Media == "" {
					return fmt.Errorf("mpd: parse: representation %q has empty media template", rep.ID)
				}
			}
		}
	}
	return nil
}
