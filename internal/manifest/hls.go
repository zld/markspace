package manifest

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediasync/internal/mederr"
)

// hlsSegment carries one parsed playlist line plus its tags.
type hlsSegment struct {
	uri       string
	sequence  int
	durationS float64
	disco     bool
	discoSeq  int
}

// ParseHLS parses a media playlist containing zero or more
// EXT-X-DISCONTINUITY tags. Local clocks reset at each discontinuity while
// the global nanosecond timeline keeps advancing by the declared segment
// durations. The first segment immediately after a discontinuity is marked
// Isolated when it carries no usable local instant, never guessed.
func ParseHLS(r io.Reader, timescale uint32) (*Timeline, error) {
	if timescale == 0 {
		return nil, mederr.New(mederr.CodeZeroTimescale,
			"HLS input needs an explicit reference timescale")
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var (
		segments                  []hlsSegment
		mediaSeq                  = 1
		haveSeq                   bool
		discoSeq                  int
		pendingDisco              bool
		headerOK                  bool
		lastDur                   float64
	)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch {
		case line == "#EXTM3U":
			headerOK = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := parseTagInt(line)
			if err != nil {
				return nil, invalidHLS(err.Error())
			}
			mediaSeq, haveSeq = v, true
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			v, err := parseTagInt(line)
			if err != nil {
				return nil, invalidHLS(err.Error())
			}
			discoSeq = v
		case line == "#EXT-X-DISCONTINUITY":
			pendingDisco = true
		case strings.HasPrefix(line, "#EXTINF:"):
			d, err := parseExtInf(line)
			if err != nil {
				return nil, invalidHLS(err.Error())
			}
			lastDur = d
		case strings.HasPrefix(line, "#"):
			// Unsupported/irrelevant tags are ignored deliberately.
		default:
			if !headerOK {
				return nil, invalidHLS("playlist does not start with #EXTM3U")
			}
			seg := hlsSegment{
				uri: line, sequence: mediaSeq, durationS: lastDur,
				disco: pendingDisco,
			}
			if pendingDisco {
				seg.discoSeq = discoSeq
				discoSeq++
				pendingDisco = false
			}
			segments = append(segments, seg)
			mediaSeq++
			lastDur = 0
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, invalidHLS("read error: " + err.Error())
	}
	if !headerOK {
		return nil, invalidHLS("playlist does not start with #EXTM3U")
	}
	if len(segments) == 0 {
		return nil, invalidHLS("playlist contains no segments")
	}
	if !haveSeq {
		mediaSeq = 1
	}
	return buildHLSTimeline(segments, timescale)
}

func buildHLSTimeline(segments []hlsSegment, timescale uint32) (*Timeline, error) {
	out := &Timeline{}
	globalNS := int64(0)
	for _, s := range segments {
		if s.durationS <= 0 {
			return nil, mederr.New(mederr.CodeInvalidManifest,
				fmt.Sprintf("segment %d has non-positive EXTINF", s.sequence),
				mederr.WithOffset(-1))
		}
		durNS := int64(s.durationS * float64(1_000_000_000))
		ev := TimelineEvent{
			Kind:           KindHLS,
			Representation: "hls/main",
			Track:          TrackVideo,
			Timescale:      timescale,
			Segment: SegmentRef{
				Number: s.sequence, URL: s.uri, ByteOffset: -1,
			},
			StartNS:       globalNS,
			EndNS:         globalNS + durNS,
			DurationTicks: int64(s.durationS * float64(timescale)),
			Discontinuity: s.disco,
			DiscoSeq:      s.discoSeq,
			// The local clock resets at a discontinuity; until a new
			// local instant is observed the segment stays isolated
			// rather than being silently guessed onto the old clock.
			Isolated: s.disco,
		}
		if err := out.append(ev); err != nil {
			return nil, err
		}
		globalNS += durNS
	}
	return out, nil
}

func parseTagInt(line string) (int, error) {
	raw := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid integer tag %q", line)
	}
	return v, nil
}

func parseExtInf(line string) (float64, error) {
	body := strings.SplitN(line, ":", 2)[1]
	body = strings.SplitN(body, ",", 2)[0]
	d, err := strconv.ParseFloat(strings.TrimSpace(body), 64)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid EXTINF %q", line)
	}
	return d, nil
}

func invalidHLS(msg string) error {
	return mederr.New(mederr.CodeInvalidManifest, "malformed HLS: "+msg)
}
