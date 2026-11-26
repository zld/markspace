// Package events reads client-side JSONL event logs. Each line is one event
// carrying a wall-clock time, a track and either an RTP timestamp or an
// explicit nanosecond position. The reader converts everything to the shared
// integer-nanosecond base and annotates every event with the 1-based line it
// came from so a later anomaly can point back at the source.
package events

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"medtrace/internal/core"
	"medtrace/internal/manifest"

	"github.com/pion/rtp"
)

// Record is one parsed event.
type Record struct {
	// Line is the 1-based line in the JSONL file.
	Line int
	// Kind is the event name ("play", "request", "stall", ...).
	Kind string
	// WallNanos is the client wall clock in Unix nanoseconds.
	WallNanos int64
	// Track identifies the media the event refers to.
	Track core.Track
	// RTP is the parsed RTP timestamp when the event carried one.
	RTP RTPStamp
	// PositionNanos is the canonical position: for events with an RTP
	// timestamp it is a placeholder until the caller correlates with a
	// track's clock rate; for explicit positions it is the final value.
	PositionNanos core.Nanos
	// Segment is a manifest-relative segment URI when the event names one.
	Segment string
	// Byte is a byte offset inside the segment when the event refers to one.
	Byte int64
	// Detail is the original message, retained for the anomaly record.
	Detail string
}

// RTPStamp is an RTP timestamp plus the metadata needed to convert it.
type RTPStamp struct {
	// Raw is the 32-bit RTP timestamp as an unsigned value widened to int64.
	Raw int64
	// ClockRate is the RTP clock rate (Hz) the timestamp is expressed in.
	ClockRate uint32
	// HasPacket reports that a base64/hex raw RTP packet accompanied the
	// event, so Raw came from parsing the packet header rather than the
	// explicit timestamp field.
	FromPacket bool
}

// Nanos converts the RTP timestamp to Unix-style nanoseconds since the
// stream's arbitrary origin. The origin is arbitrary in RTP; correlation to
// segment time happens in the runner via the first anchor.
func (r RTPStamp) Nanos() (core.Nanos, error) {
	if r.ClockRate == 0 {
		return 0, fmt.Errorf("RTP timestamp has no clock rate")
	}
	return core.FromTicks(r.Raw, r.ClockRate)
}

// Log is the whole parsed event file, sorted by wall clock and then line.
type Log struct {
	File    string
	Records []Record
}

// wire is the JSON shape accepted on each line. Optional fields let a client
// log either the RTP packet or a pre-extracted timestamp.
type wire struct {
	Kind       string  `json:"kind"`
	Event      string  `json:"event"`
	WallNS     *int64  `json:"wall_unix_nanos"`
	WallMS     *int64  `json:"wall_unix_ms"`
	WallRFC    string  `json:"wall_rfc3339"`
	Track      string  `json:"track"`
	TrackID    *uint32 `json:"track_id"`
	RepID      string  `json:"representation"`
	RTPTS      *int64  `json:"rtp_timestamp"`
	ClockRate  *uint32 `json:"rtp_clock_rate"`
	RTPPacket  string  `json:"rtp_packet"`
	PositionNS *int64  `json:"position_nanos"`
	Segment    string  `json:"segment"`
	Byte       *int64  `json:"byte_offset"`
	Detail     string  `json:"detail"`
}

// ReadFile reads and parses a JSONL event file rooted inside root.
func ReadFile(root, rel string) (*Log, error) {
	abs, err := manifest.Abs(root, rel)
	if err != nil {
		return nil, core.NewFault(core.KindPath, core.Pos{File: rel, Byte: -1}, "events", err.Error())
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, core.WrapFault(err, core.KindIO, core.Pos{File: rel, Byte: -1}, "events", "open event log")
	}
	defer f.Close()
	log, ferr := Read(rel, f)
	if ferr != nil {
		return nil, ferr
	}
	return log, nil
}

// Read parses JSONL from r.
func Read(rel string, r io.Reader) (*Log, error) {
	log := &Log{File: rel}
	sc := bufio.NewScanner(r)
	// Event lines can embed RTP packet hex; allow generously large lines.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		rec, err := parseLine(rel, line, raw)
		if err != nil {
			return nil, err
		}
		log.Records = append(log.Records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, core.WrapFault(err, core.KindIO, core.Pos{File: rel, Line: line, Byte: -1}, "events", "read event log")
	}
	sort.SliceStable(log.Records, func(i, j int) bool {
		a, b := log.Records[i], log.Records[j]
		if a.WallNanos != b.WallNanos {
			return a.WallNanos < b.WallNanos
		}
		return a.Line < b.Line
	})
	return log, nil
}

// parseLine decodes one JSON object.
func parseLine(rel string, line int, raw string) (Record, error) {
	pos := core.Pos{File: rel, Line: line, Byte: -1}
	var w wire
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Record{}, core.WrapFault(err, core.KindManifest, pos, "events", "invalid JSON event")
	}
	rec := Record{Line: line, Byte: -1, Detail: w.Detail}
	rec.Kind = strings.TrimSpace(w.Kind)
	if rec.Kind == "" {
		rec.Kind = strings.TrimSpace(w.Event)
	}
	if rec.Kind == "" {
		return Record{}, core.NewFault(core.KindManifest, pos, "events", "event has no kind")
	}
	wall, err := wallNanos(&w)
	if err != nil {
		return Record{}, core.NewFault(core.KindManifest, pos, "events", err.Error())
	}
	rec.WallNanos = wall
	rec.Track, err = wireTrack(&w, pos)
	if err != nil {
		return Record{}, err
	}
	if w.Segment != "" {
		seg, f := manifest.ResolveRooted(rel, w.Segment)
		if f != nil {
			return Record{}, &core.Fault{Kind: f.Kind, Pos: pos, Op: "events", Detail: f.Detail}
		}
		rec.Segment = seg
	}
	if w.Byte != nil {
		if *w.Byte < 0 {
			return Record{}, core.NewFault(core.KindManifest, pos, "events", "negative byte offset")
		}
		rec.Byte = *w.Byte
	}
	if err := fillTime(&w, &rec, pos); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// fillTime resolves the event's position from an explicit field, a raw
// packet, or a bare RTP timestamp. Only one source may be present.
func fillTime(w *wire, rec *Record, pos core.Pos) error {
	sources := 0
	if w.PositionNS != nil {
		sources++
	}
	if w.RTPPacket != "" {
		sources++
	}
	if w.RTPTS != nil {
		sources++
	}
	if sources > 1 {
		return core.NewFault(core.KindManifest, pos, "events", "event specifies more than one of position_nanos, rtp_packet, rtp_timestamp")
	}
	switch {
	case w.PositionNS != nil:
		if *w.PositionNS < 0 {
			return core.NewFault(core.KindManifest, pos, "events", "negative position_nanos")
		}
		rec.PositionNanos = core.Nanos(*w.PositionNS)
		return nil
	case w.RTPPacket != "":
		raw, err := decodePacket(w.RTPPacket)
		if err != nil {
			return core.NewFault(core.KindManifest, pos, "events", "invalid rtp_packet: "+err.Error())
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return core.NewFault(core.KindManifest, pos, "events", "invalid RTP header: "+err.Error())
		}
		rec.RTP = RTPStamp{Raw: int64(pkt.Timestamp), ClockRate: clockRate(w), FromPacket: true}
	case w.RTPTS != nil:
		if *w.RTPTS < 0 {
			return core.NewFault(core.KindManifest, pos, "events", "negative rtp_timestamp")
		}
		if *w.RTPTS > 1<<32-1 {
			return core.NewFault(core.KindManifest, pos, "events", "rtp_timestamp exceeds 32 bits")
		}
		rec.RTP = RTPStamp{Raw: *w.RTPTS, ClockRate: clockRate(w)}
	}
	return nil
}

// clockRate returns the declared RTP clock rate or the media-type default.
func clockRate(w *wire) uint32 {
	if w.ClockRate != nil && *w.ClockRate > 0 {
		return *w.ClockRate
	}
	switch strings.ToLower(w.Track) {
	case "audio":
		return 48000
	case "video":
		return 90000
	}
	if w.TrackID != nil {
		return 90000
	}
	return 90000
}

// decodePacket accepts base64 (standard or URL) or hex padding-free input.
func decodePacket(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) > 0 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) > 0 {
		return b, nil
	}
	if b, err := hex.DecodeString(strings.TrimPrefix(s, "0x")); err == nil && len(b) > 0 {
		return b, nil
	}
	return nil, fmt.Errorf("neither base64 nor hex")
}

// wallNanos resolves exactly one wall-clock field.
func wallNanos(w *wire) (int64, error) {
	set := 0
	if w.WallNS != nil {
		set++
	}
	if w.WallMS != nil {
		set++
	}
	if w.WallRFC != "" {
		set++
	}
	if set != 1 {
		return 0, fmt.Errorf("event must set exactly one of wall_unix_nanos, wall_unix_ms, wall_rfc3339")
	}
	switch {
	case w.WallNS != nil:
		return *w.WallNS, nil
	case w.WallMS != nil:
		return *w.WallMS * int64(core.Millisecond), nil
	default:
		n, err := parseRFC3339(w.WallRFC)
		if err != nil {
			return 0, err
		}
		return n, nil
	}
}

// wireTrack maps the string/media-type fields to a core.Track.
func wireTrack(w *wire, pos core.Pos) (core.Track, error) {
	var kind core.TrackKind
	switch strings.ToLower(strings.TrimSpace(w.Track)) {
	case "video", "v":
		kind = core.Video
	case "audio", "a":
		kind = core.Audio
	case "":
		kind = core.Other
	default:
		kind = core.Other
	}
	t := core.Track{Kind: kind, Representation: w.RepID}
	if w.TrackID != nil {
		t.ID = *w.TrackID
	}
	return t, nil
}

// parseRFC3339 reuses the manifest package's exact converter so event and
// playlist walls agree on the sub-second result.
func parseRFC3339(s string) (int64, error) {
	return manifest.ParseRFC3339Nanos(s)
}
