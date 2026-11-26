// Package subtitle parses SRT and WebVTT cues into the unified media time
// base. Every cue carries exact rational start/end times and every malformed
// timestamp is reported as a structured error, never silently coerced.
package subtitle

import (
	"bufio"
	"bytes"
	"io"
	"strings"

	"regress/internal/media"
)

// Cue is a single subtitle entry on the unified media time line.
type Cue struct {
	ID    string    // sequence identifier (SRT index or VTT id)
	Start media.Rat // exact media time, seconds
	End   media.Rat // exact media time, seconds
	Text  string    // payload body, joined lines kept verbatim
}

// Document is a parsed subtitle asset plus its two independent content
// digests: payload (cue text) and container (framing/structure).
type Document struct {
	Format           string // "srt" or "vtt"
	Cues             []Cue
	PayloadDigest    string
	ContainerDigest  string
}

// Parse decodes an SRT or WebVTT stream. format is "srt" or "vtt".
// The reader is bounded by the 64 MiB input budget.
func Parse(r io.Reader, format string) (*Document, error) {
	data, err := media.ReadAllLimited(r, media.MaxInputBytes)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(format) {
	case "srt":
		return parseSRT(data)
	case "vtt":
		return parseVTT(data)
	default:
		return nil, media.InvalidInput("unknown subtitle format %q", format)
	}
}

// parseSRT parses SubRip text: blank-line separated blocks of
// "index", "HH:MM:SS,mmm --> HH:MM:SS,mmm", then text lines.
func parseSRT(data []byte) (*Document, error) {
	blocks := splitBlocks(data)
	doc := &Document{Format: "srt"}
	pay := media.NewDigester(media.KindPayload)
	cont := media.NewDigester(media.KindContainer)
	cont.WriteString("srt\n")
	for bi, block := range blocks {
		if len(block) == 0 {
			continue
		}
		id := strings.TrimSpace(block[0])
		var timing string
		textStart := 1
		if strings.Contains(block[0], "-->") {
			timing = strings.TrimSpace(block[0])
			textStart = 1
			id = itoa(bi + 1)
		} else {
			if len(block) < 2 {
				return nil, media.InvalidTime("", "SRT block missing timing line")
			}
			timing = strings.TrimSpace(block[1])
			textStart = 2
		}
		start, end, err := parseArrow(timing, media.ParseSRTTime)
		if err != nil {
			return nil, err
		}
		text := strings.Join(block[textStart:], "\n")
		if strings.TrimSpace(text) == "" {
			return nil, media.InvalidInput("SRT cue %s has empty text", id)
		}
		cue := Cue{ID: id, Start: start, End: end, Text: text}
		if err := validateCue(cue); err != nil {
			return nil, err
		}
		doc.Cues = append(doc.Cues, cue)
		writeDigest(pay, cue.Text)
		cont.WriteString(cue.ID + "\n")
	}
	if len(doc.Cues) == 0 {
		return nil, media.InvalidInput("SRT document contains no cues")
	}
	doc.PayloadDigest = pay.Sum()
	doc.ContainerDigest = cont.Sum()
	return doc, nil
}

// parseVTT parses a WebVTT stream: a "WEBVTT" header, then blocks whose
// timing line is "HH:MM:SS.mmm --> ..." (an optional cue id may precede it).
func parseVTT(data []byte) (*Document, error) {
	blocks := splitBlocks(data)
	if len(blocks) == 0 || !strings.HasPrefix(strings.TrimSpace(blocks[0][0]), "WEBVTT") {
		return nil, media.InvalidInput("WebVTT document must start with a WEBVTT header")
	}
	doc := &Document{Format: "vtt"}
	pay := media.NewDigester(media.KindPayload)
	cont := media.NewDigester(media.KindContainer)
	cont.WriteString("vtt\n")
	seq := 0
	for _, block := range blocks[1:] {
		if len(block) == 0 {
			continue
		}
		id := ""
		timingIdx := 0
		if !strings.Contains(block[0], "-->") {
			id = strings.TrimSpace(block[0])
			timingIdx = 1
		}
		if timingIdx >= len(block) {
			return nil, media.InvalidInput("WebVTT block missing timing line")
		}
		start, end, err := parseArrow(strings.TrimSpace(block[timingIdx]), media.ParseVTTTime)
		if err != nil {
			return nil, err
		}
		seq++
		if id == "" {
			id = itoa(seq)
		}
		text := strings.Join(block[timingIdx+1:], "\n")
		if strings.TrimSpace(text) == "" {
			return nil, media.InvalidInput("WebVTT cue %s has empty text", id)
		}
		cue := Cue{ID: id, Start: start, End: end, Text: text}
		if err := validateCue(cue); err != nil {
			return nil, err
		}
		doc.Cues = append(doc.Cues, cue)
		writeDigest(pay, cue.Text)
		cont.WriteString(cue.ID + "\n")
	}
	if len(doc.Cues) == 0 {
		return nil, media.InvalidInput("WebVTT document contains no cues")
	}
	doc.PayloadDigest = pay.Sum()
	doc.ContainerDigest = cont.Sum()
	return doc, nil
}

// parseArrow splits "start --> end" and parses both ends with the supplied
// time parser. Settings after the end time are tolerated but not folded in.
func parseArrow(line string, parse func(string) (media.Rat, error)) (media.Rat, media.Rat, error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return media.Rat{}, media.Rat{}, media.InvalidTime(line, "cue timing must contain -->")
	}
	start, err := parse(strings.TrimSpace(parts[0]))
	if err != nil {
		return media.Rat{}, media.Rat{}, err
	}
	endField := strings.TrimSpace(parts[1])
	// WebVTT allows cue settings after the end timestamp; take the first token.
	if i := strings.IndexAny(endField, " \t"); i >= 0 {
		endField = endField[:i]
	}
	end, err := parse(endField)
	if err != nil {
		return media.Rat{}, media.Rat{}, err
	}
	return start, end, nil
}

func validateCue(c Cue) error {
	if c.End.Cmp(c.Start) <= 0 {
		return media.InvalidTime(c.ID+" timing", "cue end must be strictly after start")
	}
	return nil
}

func splitBlocks(data []byte) [][]string {
	blocks := [][]string{}
	cur := []string{}
	sc := bufio.NewScanner(bytes.NewReader(normalizeNewlines(data)))
	sc.Buffer(make([]byte, 1024*1024), int(media.MaxInputBytes))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

func normalizeNewlines(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

func writeDigest(d *media.Digester, s string) {
	d.WriteString(s)
	d.WriteString("\x00")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
