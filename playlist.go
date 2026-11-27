package mediacore

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Format identifies a manifest syntax.
type Format string

const (
	// FormatHLS is an extended M3U playlist with X-MEDIACORE-* tags.
	FormatHLS Format = "hls"
	// FormatDASH is a minimal MPD-like XML document.
	FormatDASH Format = "dash"
)

// SegmentRef is one fragment reference parsed from a manifest: its media
// sequence number, declared duration and source position.
type SegmentRef struct {
	URI       string
	Seq       uint32
	Duration  Nano
	Timescale uint32
	Line      int    // 1-based source line (HLS); 1 for DASH elements
	ByteOff   int64  // 0-based byte offset of the declaring line/element
	Kind      string // "media" or "rtp"
	Attrs     map[string]string
}

// TrackDecl is one playable track as declared by a manifest.
type TrackDecl struct {
	ID         uint32
	Kind       string // audio|video
	Timescale  uint32
	Version    string
	DependsOn  []uint32
	Rate       uint32 // declared audio sample rate; 0 = not an audio/rate track
	Layout     string // declared channel layout name; "" = none
	TargetRate uint32 // target sample rate for layout/rate conversion; 0 = native
	Target     string // target channel layout name; "" = native
	ColorPrio  uint32 // explicit color metadata priority; 0 = lowest
	Segments   []*SegmentRef
	Line       int
	ByteOff    int64
}

// Playlist is the parsed, position-annotated view of one manifest.
type Playlist struct {
	AssetID string
	Format  Format
	Tracks  []*TrackDecl
}

// ParsePlaylist sniffs the syntax from the first non-blank line and parses r.
// Every returned rejection is a *Error anchored at the offending position.
func ParsePlaylist(asset string, r io.Reader) (*Playlist, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, Wrap(err, asset, 0, StagePlaylist, -1)
	}
	head := strings.TrimSpace(string(raw[:min(len(raw), 256)]))
	switch {
	case strings.HasPrefix(head, "#EXTM3U"):
		return parseHLS(asset, raw)
	case strings.HasPrefix(head, "<?xml") || strings.HasPrefix(head, "<MPD"):
		return parseDASH(asset, raw)
	default:
		return nil, WrapMsg(ErrIllegalField, asset, 0, StagePlaylist, 0,
			"unrecognized manifest header")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseDecimalSeconds turns an EXTINF duration ("9.0", "0.02") into exact
// nanoseconds. More than nine fractional digits would lose precision and are
// rejected; so are empty, non-numeric or overflowing values.
func parseDecimalSeconds(asset string, tok string, line int, off int64) (Nano, error) {
	if tok == "" {
		return 0, illegal(asset, "empty EXTINF duration", line, off)
	}
	neg := false
	s := tok
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
	}
	parts := strings.SplitN(s, ".", 2)
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || parts[0] == "" {
		return 0, illegal(asset, "bad EXTINF duration: "+tok, line, off)
	}
	var frac uint64
	if len(parts) == 2 {
		f := parts[1]
		if f == "" || len(f) > 9 {
			return 0, illegal(asset, "EXTINF fraction must have 1..9 digits: "+tok, line, off)
		}
		frac, err = strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, illegal(asset, "bad EXTINF fraction: "+tok, line, off)
		}
		for i := len(f); i < 9; i++ {
			frac *= 10
		}
	}
	if whole > uint64(maxNano)/uint64(NanosPerSecond) {
		return 0, WrapMsg(ErrOverflow, asset, 0, StagePlaylist, off, "duration overflow")
	}
	ns := Nano(whole)*NanosPerSecond + Nano(frac)
	if neg {
		return 0, illegal(asset, "negative EXTINF duration: "+tok, line, off)
	}
	if ns <= 0 {
		return 0, illegal(asset, "zero EXTINF duration: "+tok, line, off)
	}
	return ns, nil
}

func illegal(asset, msg string, line int, off int64) *Error {
	e := WrapMsg(ErrIllegalField, asset, 0, StagePlaylist, off, msg)
	e.Msg = fmt.Sprintf("line %d: %s", line, msg)
	return e
}

// parseTagAttrs parses a comma-free KEY=VALUE attribute list ("A=1,B=x").
func parseTagAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[strings.TrimSpace(kv[:i])] = strings.Trim(strings.TrimSpace(kv[i+1:]), `"`)
		}
	}
	return out
}

func attrOne(attrs map[string]string, key, fallback string) string {
	if v, ok := attrs[key]; ok && v != "" {
		return v
	}
	return fallback
}

func reqUint(asset string, a map[string]string, key string, line int, off int64) (uint32, error) {
	v, ok := a[key]
	if !ok || v == "" {
		return 0, illegal(asset, "missing required attribute "+key, line, off)
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0, illegal(asset, "bad integer attribute "+key+"="+v, line, off)
	}
	return uint32(n), nil
}

func optUint(a map[string]string, key string) uint32 {
	v, ok := a[key]
	if !ok {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}
