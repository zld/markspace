package demo

import (
	"fmt"
	"path/filepath"
	"strconv"
)

// dashSpec defines one generated representation.
type dashSpec struct {
	id        string
	timescale uint32
	trackID   uint32
	segments  int
	duration  int64 // ticks per segment
}

// generateDASH writes a two-representation DASH presentation with aligned
// per-segment boundaries (90 kHz high vs 30 kHz low, identical wall time).
func (b *Bundle) generateDASH() error {
	specs := []dashSpec{
		{id: "v0", timescale: 90000, trackID: 1, segments: 4, duration: 9000},
		{id: "v1", timescale: 30000, trackID: 2, segments: 4, duration: 3000},
	}
	var mpd []byte
	mpd = append(mpd, []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" minBufferTime="PT1.5S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
`)...)
	for _, spec := range specs {
		mpd = append(mpd, []byte(fmt.Sprintf(
			`      <Representation id="%s" mimeType="video/mp4">
        <SegmentTemplate timescale="%d" media="$RepresentationID$-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
`, spec.id, spec.timescale))...)
		var t int64
		for i := 0; i < spec.segments; i++ {
			mpd = append(mpd, []byte(fmt.Sprintf(
				"            <S t=\"%d\" d=\"%d\"/>\n", t, spec.duration))...)
			t += spec.duration
		}
		mpd = append(mpd, []byte("          </SegmentTimeline>\n        </SegmentTemplate>\n      </Representation>\n")...)
	}
	mpd = append(mpd, []byte("    </AdaptationSet>\n  </Period>\n</MPD>\n")...)

	mpdPath := filepath.Join(b.Root, DirDASH, MPDName)
	if err := writeFile(mpdPath, mpd); err != nil {
		return err
	}
	b.ManifestDASH = mpd

	for _, spec := range specs {
		var t int64
		for i := 0; i < spec.segments; i++ {
			// Distinct but deterministic payload per representation/segment.
			data := mp4Segment(spec.trackID, spec.timescale, 4, 16, 2)
			name := spec.id + "-" + strconv.Itoa(i+1) + ".m4s"
			path := filepath.Join(b.Root, DirDASH, name)
			if err := writeFile(path, data); err != nil {
				return err
			}
			b.Segments = append(b.Segments, Segment{
				Path:         path,
				Source:       filepath.Join(DirDASH, name),
				RepID:        spec.id,
				SegmentIndex: int64(i),
				TrackID:      spec.trackID,
				Timescale:    spec.timescale,
				TimeStart:    t,
				TimeEnd:      t + spec.duration,
				Keyframe:     true,
				ExpectedHash: expectedRecord(data),
				ByteLength:   int64(len(data)),
			})
			t += spec.duration
		}
	}
	return nil
}
