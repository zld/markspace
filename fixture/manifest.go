package fixture

import "fmt"

// BuildMPD returns a static two-representation DASH manifest. When
// timingJump is true the SegmentTimeline contains an unexplained hole
// (previous end 8000, next start 10000) that the analyzer must record.
func BuildMPD(timingJump bool) string {
	second := `<S t="10000" d="2000"/>`
	if !timingJump {
		second = `<S d="2000"/>`
	}
	timeline := `<S t="0" d="2000" r="3"/>` + second
	mpd := `<?xml version="1.0" encoding="UTF-8"?>
<MPD type="static" mediaPresentationDuration="PT12S">
  <Period>
    <AdaptationSet mimeType="video/mp4" contentType="video">
%s
%s
    </AdaptationSet>
  </Period>
</MPD>
`
	rep := `      <Representation id="%s" bandwidth="%d">
        <SegmentTemplate timescale="1000" startNumber="1" media="%s/$Number$.m4s" initialization="%s/init.mp4">
          <SegmentTimeline>%s</SegmentTimeline>
        </SegmentTemplate>
      </Representation>`
	r1 := fmt.Sprintf(rep, "v800", 800000, "v800", "v800", timeline)
	r2 := fmt.Sprintf(rep, "v1600", 1600000, "v1600", "v1600", timeline)
	return fmt.Sprintf(mpd, r1, r2)
}

// BuildHLS returns a media playlist with one EXT-X-DISCONTINUITY: the
// discontinuity opens a new continuity interval and must not be reported
// as a missing segment.
func BuildHLS() string {
	return `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:40
#EXTINF:2.000,
seg40.ts
#EXTINF:2.000,
seg41.ts
#EXT-X-DISCONTINUITY
#EXTINF:2.000,
seg42.ts
#EXTINF:2.000,
seg43.ts
#EXT-X-ENDLIST
`
}

// BuildLegacy returns the old-format manifest that the migration stage
// upgrades to the normalized JSON timeline.
func BuildLegacy() string {
	return `STREAMCHECK-LEGACY-V1
SEG 0 2000 seg0.ts
SEG 2000 2000 seg1.ts
DISCONTINUITY
SEG 4000 2000 seg2.ts
SEG 6000 2000 seg3.ts
END
`
}
