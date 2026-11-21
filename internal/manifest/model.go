// Package manifest defines the playlist data model and the naming/M3U8 rules
// shared by the generator and by the verifier that re-reads generated files.
package manifest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DefaultPattern is the segment naming rule used when --pattern is omitted.
const DefaultPattern = "seg_{rate}_{index}.ts"

// Segment is one media segment on disk together with the facts gathered while
// scanning and probing. DurationMS is zero until a successful probe.
type Segment struct {
	ID              int
	RateKbps        int
	Path            string
	FileName        string
	DurationMS      int64
	SizeBytes       int64
	ModTimeUnixNano int64
	ProbeError      string
}

// Usable reports whether the segment can be emitted into a playlist. Segments
// with a failed or zero-duration probe are reported but never fabricated.
func (s Segment) Usable() bool { return s.ProbeError == "" && s.DurationMS > 0 }

// PlaylistSegment is one entry of a media playlist.
type PlaylistSegment struct {
	ID         int
	FileName   string
	DurationMS int64
}

// Playlist is a VOD media playlist for one bitrate.
type Playlist struct {
	RateKbps       int
	Version        int
	TargetDuration int
	MediaSequence  int
	Segments       []PlaylistSegment
}

// TotalDurationMS is the sum of all entry durations in milliseconds.
func (p Playlist) TotalDurationMS() int64 {
	var total int64
	for _, seg := range p.Segments {
		total += seg.DurationMS
	}
	return total
}

// FormatEXTINF renders a duration at millisecond precision without floating
// point arithmetic, so 4000 always becomes "4.000".
func FormatEXTINF(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

// TargetDurationSeconds is the HLS EXT-X-TARGETDURATION value: the largest
// EXTINF rounded up to a whole second (minimum 1).
func TargetDurationSeconds(segments []PlaylistSegment) int {
	var maxMS int64
	for _, seg := range segments {
		if seg.DurationMS > maxMS {
			maxMS = seg.DurationMS
		}
	}
	if maxMS <= 0 {
		return 1
	}
	return int((maxMS + 999) / 1000)
}

// SegmentFileName expands a validated pattern for a rate and segment number.
func SegmentFileName(pattern string, rate, id int) string {
	name := strings.ReplaceAll(pattern, "{rate}", strconv.Itoa(rate))
	return strings.ReplaceAll(name, "{index}", strconv.Itoa(id))
}

// Pattern is a compiled segment naming rule such as "seg_{rate}_{index}.ts".
type Pattern struct {
	raw string
	re  *regexp.Regexp
}

// CompilePattern validates a naming rule and compiles it for matching. Both
// placeholders expand to unsigned decimal digit runs; {index} is required.
func CompilePattern(pattern string) (*Pattern, error) {
	if pattern == "" {
		return nil, fmt.Errorf("segment pattern is empty")
	}
	if pattern != strings.TrimSpace(pattern) {
		return nil, fmt.Errorf("segment pattern must not contain surrounding whitespace")
	}
	if pattern != baseName(pattern) {
		return nil, fmt.Errorf("segment pattern %q must be a bare file name, not a path", pattern)
	}
	if strings.Count(pattern, "{") != strings.Count(pattern, "}") {
		return nil, fmt.Errorf("segment pattern %q has an unclosed placeholder", pattern)
	}
	seen := map[string]bool{}
	for _, ph := range placeholderRE.FindAllString(pattern, -1) {
		switch ph {
		case "{rate}", "{index}":
			if seen[ph] {
				return nil, fmt.Errorf("segment pattern %q repeats placeholder %s", pattern, ph)
			}
			seen[ph] = true
		default:
			return nil, fmt.Errorf("segment pattern %q has unknown placeholder %s", pattern, ph)
		}
	}
	if !seen["{index}"] {
		return nil, fmt.Errorf("segment pattern %q must contain {index}", pattern)
	}
	expr := regexp.QuoteMeta(pattern)
	expr = strings.ReplaceAll(expr, regexp.QuoteMeta("{rate}"), `(?P<rate>[0-9]+)`)
	expr = strings.ReplaceAll(expr, regexp.QuoteMeta("{index}"), `(?P<index>[0-9]+)`)
	re, err := regexp.Compile("^" + expr + "$")
	if err != nil {
		return nil, fmt.Errorf("segment pattern %q is not valid: %w", pattern, err)
	}
	return &Pattern{raw: pattern, re: re}, nil
}

var placeholderRE = regexp.MustCompile(`\{[^}]*\}`)

// HasRate reports whether the rule carries a {rate} placeholder. Without it a
// single-rate build may assign all matches to the requested rate.
func (p *Pattern) HasRate() bool { return strings.Contains(p.raw, "{rate}") }

// Match extracts the rate and segment number from a file name. A false result
// means the name does not follow the rule and must be ignored. Rate is -1 when
// the rule has no {rate} placeholder.
func (p *Pattern) Match(name string) (rate, id int, ok bool) {
	m := p.re.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, false
	}
	rate = -1
	for i, group := range p.re.SubexpNames() {
		if i == 0 || group == "" {
			continue
		}
		n, err := strconv.Atoi(m[i])
		if err != nil {
			return 0, 0, false
		}
		switch group {
		case "rate":
			rate = n
		case "index":
			id = n
		}
	}
	return rate, id, true
}

func baseName(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return p
	}
	return p[i+1:]
}
