package fixture

import (
	"fmt"
	"strings"
)

// Representation describes one generated DASH rendition.
type Representation struct {
	ID        string
	Bandwidth int64
	MimeType  string
	Codecs    string
	Timescale uint32
	// SegTicks is the segment duration in the representation's timescale.
	SegTicks uint64
	// BoundariesMS are the HLS interval boundaries this rendition produces.
	BoundariesMS []int
	Segments     []FragmentSpec
}

// Scenario is one complete, self-contained delivery case.
type Scenario struct {
	Name         string
	MPD          []byte
	Playlist     []byte
	Media        map[string][]byte
	BoundariesMS []int
}

// MediaSequenceBase is the first HLS media sequence number used everywhere.
const MediaSequenceBase = uint64(100)

// HLSBoundaries is the interval grid shared by every generated rendition:
// four 2-second media segments over an 8-second presentation, with a
// discontinuity between the second and third segments.
var HLSBoundaries = []int{0, 2000, 4000, 6000, 8000}

// PlaylistFor renders the media playlist for the scenario's boundary grid.
// When repeatedSeq is true the second interval restarts at the same media
// sequence number, which the parser must diagnose rather than reorder.
func PlaylistFor(boundaries []int, discontinuityAt int, repeatedSeq bool) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n")
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", MediaSequenceBase)
	b.WriteString("#EXT-X-DISCONTINUITY-SEQUENCE:0\n")
	for i := 0; i+1 < len(boundaries); i++ {
		if i == discontinuityAt {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		dur := float64(boundaries[i+1]-boundaries[i]) / 1000
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", dur)
		fmt.Fprintf(&b, "seg-%d.m4s\n", i)
	}
	if repeatedSeq {
		// A hand-edited playlist that restarts the sequence counter after
		// the discontinuity. The parser must report this and keep file
		// order; it must never silently renumber.
		insert := strings.Index(b.String(), "#EXT-X-DISCONTINUITY\n")
		out := b.String()
		tag := fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", MediaSequenceBase+uint64(discontinuityAt))
		out = out[:insert] + tag + out[insert:]
		return []byte(out)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return []byte(b.String())
}

// MPDEscaped is a minimal MPD for the given renditions.
func MPDEscaped(periodDuration string, reps []Representation) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="%s">`+"\n", periodDuration)
	fmt.Fprintf(&b, `  <Period id="p0" start="PT0S" duration="%s">`+"\n", periodDuration)
	for _, r := range reps {
		fmt.Fprintf(&b, "    <AdaptationSet contentType=\"%s\" mimeType=\"%s\">\n", contentType(r.MimeType), r.MimeType)
		fmt.Fprintf(&b, "      <SegmentTemplate timescale=\"%d\" duration=\"%d\" startNumber=\"1\" media=\"%s/$Number$.m4s\"/>\n",
			r.Timescale, r.SegTicks, r.ID)
		fmt.Fprintf(&b, "      <Representation id=\"%s\" bandwidth=\"%d\" codecs=\"%s\"/>\n", r.ID, r.Bandwidth, r.Codecs)
		b.WriteString("    </AdaptationSet>\n")
	}
	b.WriteString("  </Period>\n</MPD>\n")
	return []byte(b.String())
}

// MPDWithFaults renders the same MPD but with the five injectable defects
// used by the demo. Which faults are active is selected by name.
type FaultKind string

const (
	FaultTruncatedBox      FaultKind = "truncated_box"
	FaultInvalidBoxSize    FaultKind = "invalid_box_size"
	FaultZeroTimescale     FaultKind = "zero_timescale"
	FaultMissingIndex      FaultKind = "missing_index"
	FaultDuplicateSequence FaultKind = "duplicate_sequence"
)

// AllFaults is the canonical fault order used by the demo.
var AllFaults = []FaultKind{FaultTruncatedBox, FaultInvalidBoxSize, FaultZeroTimescale,
	FaultDuplicateSequence, FaultMissingIndex}

// FaultyMPD injects the two MPD-side defects into a four-representation
// ladder:
//
//	zero_timescale  v360's SegmentTemplate declares timescale 0
//	missing_index   a48 has no SegmentTemplate at all
//
// v1080 and v720 keep valid indexes so the media-side defects can still be
// reached by the scanner.
func FaultyMPD(zeroTimescale, missingIndex bool) []byte {
	v360Template := `<SegmentTemplate timescale="90000" duration="72000" startNumber="1" media="v360/$Number$.m4s"/>`
	if zeroTimescale {
		v360Template = `<SegmentTemplate timescale="0" duration="72000" startNumber="1" media="v360/$Number$.m4s"/>`
	}
	a48Template := `<SegmentTemplate timescale="48000" duration="96000" startNumber="1" media="a48/$Number$.m4s"/>`
	if missingIndex {
		a48Template = `<!-- the audio index was never written -->`
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT8S">
  <Period id="p0" start="PT0S" duration="PT8S">
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" startNumber="1" media="v1080/$Number$.m4s"/>
      <Representation id="v1080" bandwidth="5000000" codecs="avc1.640028"/>
    </AdaptationSet>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="1000" duration="2000" startNumber="1" media="v720/$Number$.m4s"/>
      <Representation id="v720" bandwidth="2500000" codecs="avc1.4d401f"/>
    </AdaptationSet>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      %s
      <Representation id="v360" bandwidth="800000" codecs="avc1.4d401e"/>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4">
      %s
      <Representation id="a48" bandwidth="128000" codecs="mp4a.40.2"/>
    </AdaptationSet>
  </Period>
</MPD>
`, v360Template, a48Template))
}

func contentType(mime string) string {
	if strings.HasPrefix(mime, "audio/") {
		return "audio"
	}
	return "video"
}

// SegmentsFor builds exactly one fragment per HLS interval. The fragment
// sample table is sized so the decoded duration matches the interval at the
// given timescale.
func SegmentsFor(boundaries []int, timescale uint32, trackID uint32, samples int) []FragmentSpec {
	var specs []FragmentSpec
	for i := 0; i+1 < len(boundaries); i++ {
		spanMS := boundaries[i+1] - boundaries[i]
		if samples <= 0 {
			samples = 1
		}
		totalTicks := uint64(spanMS) * uint64(timescale) / 1000
		per := totalTicks / uint64(samples)
		if per == 0 {
			per = 1
		}
		specs = append(specs, FragmentSpec{
			SequenceNumber: uint32(i + 1),
			TrackID:        trackID,
			BaseDecodeTime: uint64(boundaries[i]) * uint64(timescale) / 1000,
			SampleCount:    uint32(samples),
			SampleDuration: uint32(per),
			SampleSize:     128,
			PayloadSeed:    int64(trackID)*7919 + int64(i+1),
		})
	}
	return specs
}

// sampleCountFor keeps generated sample tables small but measurable.
func sampleCountFor(timescale uint32) int {
	switch timescale {
	case 90000:
		return 3
	case 48000:
		return 2
	default:
		return 2
	}
}
