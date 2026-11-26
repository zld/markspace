package manifest

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"continuity/internal/clock"
)

// hlsLine is one parsed playlist line with its 1-based number.
type hlsLine struct {
	no   int
	text string
}

// ParseHLS reads a media (variant) playlist. Master playlists are rejected:
// the kernel consumes concrete media playlists, one Representation each.
func ParseHLS(path string) (*Manifest, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, clock.New(clock.KindIO, "parse hls", "abs path: %v", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, clock.AtFile(clock.New(clock.KindIO, "parse hls",
			"open manifest: %v", err), abs)
	}
	defer f.Close()

	scanner := bufio.NewScanner(io.LimitReader(f, MaxManifestBytes+1))
	scanner.Buffer(make([]byte, 64*1024), int(MaxManifestBytes))
	var lines []hlsLine
	for scanner.Scan() {
		lines = append(lines, hlsLine{no: len(lines) + 1, text: scanner.Text()})
	}
	if err := scanner.Err(); err != nil {
		return nil, clock.AtFile(clock.New(clock.KindIO, "parse hls",
			"read manifest: %v", err), abs)
	}
	if len(lines) == 0 {
		return nil, clock.AtFile(clock.New(clock.KindInvalidInput, "parse hls",
			"empty playlist"), abs)
	}
	if strings.TrimSpace(lines[0].text) != "#EXTM3U" {
		return nil, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
			"parse hls", "missing #EXTM3U header"), lines[0].no), abs)
	}

	rep := Representation{ID: filepath.Base(abs)}
	m := &Manifest{Kind: KindHLS, Path: abs, Dir: filepath.Dir(abs)}
	sequence := int64(0)
	customSeq := false
	pendingDisc := false
	pendingInf := false
	var pendingDur clock.Rat
	pendingLine := 0
	seenSeq := make(map[int64]int)
	seenURL := make(map[string]int)
	tCursor := clock.FromInt(0)
	idx := 0

	for _, ln := range lines[1:] {
		text := strings.TrimSpace(ln.text)
		if text == "" {
			continue
		}
		switch {
		case strings.HasPrefix(text, "#EXT-X-STREAM-INF"):
			return nil, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
				"parse hls", "master playlist not supported; pass a media playlist"),
				ln.no), abs)
		case strings.HasPrefix(text, "#EXT-X-MEDIA-SEQUENCE:"):
			v, perr := parseTagInt(text, ln.no, abs)
			if perr != nil {
				return nil, perr
			}
			sequence = v
			customSeq = true
		case strings.HasPrefix(text, "#EXT-X-TARGETDURATION:"):
			if _, perr := parseTagInt(text, ln.no, abs); perr != nil {
				return nil, perr
			}
		case strings.HasPrefix(text, "#EXT-X-DISCONTINUITY"):
			pendingDisc = true
		case strings.HasPrefix(text, "#EXTINF:"):
			dur, perr := parseEXTINF(text, ln.no, abs)
			if perr != nil {
				return nil, perr
			}
			pendingDur = dur
			pendingInf = true
			pendingLine = ln.no
		case strings.HasPrefix(text, "#"):
			// Other tags (KEY, VERSION, ALLOWED-CPC...) are tolerated.
			continue
		default:
			// Media segment URI.
			if !pendingInf {
				return nil, clock.AtFile(clock.AtLine(clock.New(
					clock.KindInvalidInput, "parse hls",
					"segment URI without preceding #EXTINF: %q", text), ln.no), abs)
			}
			if prev, dup := seenSeq[sequence]; dup {
				return nil, clock.AtFile(clock.AtLine(clock.New(
					clock.KindInvalidInput, "parse hls",
					"duplicate media sequence %d (also at index %d)", sequence, prev),
					ln.no), abs)
			}
			if prev, dup := seenURL[text]; dup {
				return nil, clock.AtFile(clock.AtLine(clock.New(
					clock.KindInvalidInput, "parse hls",
					"duplicate segment reference %q (also at index %d)", text, prev),
					ln.no), abs)
			}
			seenSeq[sequence] = idx
			seenURL[text] = idx
			if pendingDisc {
				rep.Discontinuities = append(rep.Discontinuities, idx)
				pendingDisc = false
			}
			rep.Segments = append(rep.Segments, Segment{
				Index:    idx,
				Sequence: int(sequence),
				URL:      text,
				Start:    tCursor,
				Duration: pendingDur,
				Line:     pendingLine,
				Offset:   -1,
			})
			next, err := tCursor.Add(pendingDur)
			if err != nil {
				return nil, clock.AtFile(clock.AtLine(err, pendingLine), abs)
			}
			tCursor = next
			sequence++
			idx++
			pendingInf = false
		}
	}
	if pendingInf {
		return nil, clock.AtFile(clock.New(clock.KindInvalidInput, "parse hls",
			"dangling #EXTINF without segment URI"), abs)
	}
	if len(rep.Segments) == 0 {
		return nil, clock.AtFile(clock.New(clock.KindInvalidInput, "parse hls",
			"playlist contains no segments"), abs)
	}
	_ = customSeq
	m.MediaPresentSec = tCursor
	rep.BandwidthBPS = 0
	m.Representations = []Representation{rep}
	if err := CheckSegmentFiles(m); err != nil {
		return nil, err
	}
	return m, nil
}

func parseTagInt(tag string, line int, file string) (int64, error) {
	val := strings.TrimSpace(strings.SplitN(tag, ":", 2)[1])
	v, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
			"parse hls", "bad integer %q: %v", val, err), line), file)
	}
	return v, nil
}

// parseEXTINF parses "#EXTINF:<duration>,[title]"; integer or float seconds.
func parseEXTINF(tag string, line int, file string) (clock.Rat, error) {
	body := strings.SplitN(tag, ":", 2)[1]
	durText := strings.SplitN(body, ",", 2)[0]
	durText = strings.TrimSpace(durText)
	if strings.Contains(durText, ".") {
		parts := strings.SplitN(durText, ".", 2)
		whole, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return clock.Rat{}, badEXTINF(durText, line, file, err)
		}
		scale := int64(1)
		for range parts[1] {
			scale *= 10
		}
		frac, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return clock.Rat{}, badEXTINF(durText, line, file, err)
		}
		fr, err := clock.R(frac, scale)
		if err != nil {
			return clock.Rat{}, clock.AtFile(clock.AtLine(err, line), file)
		}
		sum, err := clock.FromInt(whole).Add(fr)
		if err != nil {
			return clock.Rat{}, clock.AtFile(clock.AtLine(err, line), file)
		}
		return sum, nil
	}
	v, err := strconv.ParseInt(durText, 10, 64)
	if err != nil {
		return clock.Rat{}, badEXTINF(durText, line, file, err)
	}
	return clock.FromInt(v), nil
}

func badEXTINF(val string, line int, file string, wrap error) error {
	return clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
		"parse hls", "bad EXTINF duration %q: %v", val, wrap), line), file)
}
