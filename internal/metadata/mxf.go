// Package metadata reads container-level metadata from MXF and MP4 inputs,
// converts every time base onto the master integer clock and adjudicates
// conflicting fields with fixed priority MXF > MP4 > default.
package metadata

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"mediasync/internal/clock"
	"mediasync/internal/model"
)

// Source identifies who declared a metadata value.
type Source string

// Declaring sources, ordered by adjudication priority.
const (
	SourceMXF     Source = "mxf"
	SourceMP4     Source = "mp4"
	SourceDefault Source = "default"
)

// Track kind identifiers.
const (
	KindVideo    = "video"
	KindAudio    = "audio"
	KindSubtitle = "subtitle"
)

// MXFMagic is the fixed header marker of the line-oriented MXF interchange
// fixture format. Everything before the END line is the header region.
const MXFMagic = "MXF1\n"

// Crop is a clean-aperture crop in source pixels.
type Crop struct {
	Top    uint32 `json:"top"`
	Bottom uint32 `json:"bottom"`
	Left   uint32 `json:"left"`
	Right  uint32 `json:"right"`
}

// ColorInfo carries NCLX-style colour description.
type ColorInfo struct {
	ColorType  string `json:"color_type"`
	Primaries  uint16 `json:"primaries"`
	Transfer   uint16 `json:"transfer"`
	Matrix     uint16 `json:"matrix"`
	FullRange  bool   `json:"full_range"`
}

// Field is an adjudicated value: the winner, every originally declared value
// and a conflict flag when sources disagreed.
type Field[T comparable] struct {
	Value    T             `json:"value"`
	Winner   Source        `json:"winner"`
	Original map[Source]T  `json:"original,omitempty"`
	Conflict bool          `json:"conflict"`
}

// TrackMeta is one parsed track from one container. Declared records which
// fields were actually present, because zero is a valid value for several of
// them (e.g. rotation=0) and cannot be used as an absence marker.
type TrackMeta struct {
	TrackID       uint32            `json:"track_id"`
	Kind          string            `json:"kind"`
	Timescale     uint32            `json:"timescale"`
	Duration      int64             `json:"duration"`
	DurationTicks int64             `json:"duration_ticks"`
	Width         uint32            `json:"width,omitempty"`
	Height        uint32            `json:"height,omitempty"`
	Channels      uint32            `json:"channels,omitempty"`
	SampleRate    uint32            `json:"sample_rate,omitempty"`
	Language      string            `json:"language,omitempty"`
	Rotation      int               `json:"rotation"`
	Crop          Crop              `json:"crop"`
	Color         ColorInfo         `json:"color"`
	HeaderOffset  int64             `json:"header_offset"`
	Declared      map[string]bool   `json:"declared"`
}

// ContainerMeta is one container's parsed header metadata. Declared lists
// the fields this container actually set.
type ContainerMeta struct {
	Source        Source            `json:"source"`
	DeclaredSize  int64             `json:"declared_size"`
	Timescale     uint32            `json:"timescale"`
	Duration      int64             `json:"duration"`
	DurationTicks int64             `json:"duration_ticks"`
	Rotation      int               `json:"rotation"`
	Crop          Crop              `json:"crop"`
	Color         ColorInfo         `json:"color"`
	Tracks        []TrackMeta       `json:"tracks"`
	Declared      map[string]bool   `json:"declared"`
}

// Defaults are fallback values used when neither container declares a field.
type Defaults struct {
	Rotation  int
	Crop      Crop
	Color     ColorInfo
}

// DefaultDefaults returns the built-in fallback metadata.
func DefaultDefaults() Defaults {
	return Defaults{
		Color: ColorInfo{ColorType: "nclx", Primaries: 1, Transfer: 1, Matrix: 1, FullRange: true},
	}
}

// ParseMXF parses the line-oriented MXF interchange fixture. Truncated
// headers and illegal time stamps are recorded on rec with reviewable
// offsets; a non-nil error is returned alongside the partial result when the
// stream is unusable as a whole.
func ParseMXF(data []byte, rec *model.Recorder) (*ContainerMeta, error) {
	meta := &ContainerMeta{Source: SourceMXF, Declared: map[string]bool{}}
	if len(data) < len(MXFMagic) || string(data[:len(MXFMagic)]) != MXFMagic {
		rec.Add(model.ErrorRecord{Code: model.ErrTruncatedHeader, Track: "mxf",
			Offset: int64(len(data)), Message: "missing MXF magic marker"})
		return meta, fmt.Errorf("metadata: truncated MXF header at byte %d", len(data))
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var offset int64
	lineNo := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineStart := offset
		offset += int64(len(line)) + 1 // account for '\n'
		lineNo++
		if lineNo == 1 {
			continue // magic
		}
		if line == "END" {
			break
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "LENGTH":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				meta.DeclaredSize = n
			}
		case "TIMESCALE":
			meta.Timescale = parseUint32(val)
			meta.Declared["timescale"] = true
		case "DURATION":
			meta.Duration = parseInt64(val)
			meta.Declared["duration"] = true
		case "ROTATION":
			meta.Rotation = int(parseInt64(val))
			meta.Declared["rotation"] = true
		case "CROP":
			meta.Crop = parseCrop(val)
			meta.Declared["crop"] = true
		case "COLOR":
			meta.Color = parseColor(val)
			meta.Declared["color"] = true
		case "TRACK":
			if tr := parseTrack(val, lineStart, rec); tr != nil {
				meta.Tracks = append(meta.Tracks, *tr)
			}
		}
	}
	if meta.DeclaredSize > 0 && meta.DeclaredSize > int64(len(data)) {
		rec.Add(model.ErrorRecord{Code: model.ErrTruncatedHeader, Track: "mxf",
			Offset: int64(len(data)), StartTick: 0, EndTick: meta.DurationTicks,
			Message: fmt.Sprintf("declared %d bytes but stream ends at %d", meta.DeclaredSize, len(data))})
	}
	meta.DurationTicks = convertDuration(meta.Timescale, meta.Duration, "mxf", 0, rec)
	for i := range meta.Tracks {
		tr := &meta.Tracks[i]
		tr.DurationTicks = convertDuration(tr.Timescale, tr.Duration, tr.Kind, tr.HeaderOffset, rec)
	}
	if rec.HasError() {
		return meta, fmt.Errorf("metadata: MXF parse failed: %s", rec.JoinMessages())
	}
	return meta, nil
}

func convertDuration(timescale uint32, duration int64, track string, off int64, rec *model.Recorder) int64 {
	if timescale == 0 {
		rec.Add(model.ErrorRecord{Code: model.ErrInvalidTimestamp, Track: track, Offset: off,
			Message: "zero time scale with non-zero duration"})
		return 0
	}
	if duration < 0 {
		rec.Add(model.ErrorRecord{Code: model.ErrInvalidTimestamp, Track: track, Offset: off,
			EndTick: duration, Message: "negative duration"})
		return 0
	}
	ticks, err := clock.Scale(duration, int64(timescale))
	if err != nil {
		rec.Add(model.ErrorRecord{Code: model.ErrInvalidTimestamp, Track: track, Offset: off,
			Message: err.Error()})
		return 0
	}
	return ticks
}

func parseTrack(val string, offset int64, rec *model.Recorder) *TrackMeta {
	tr := &TrackMeta{HeaderOffset: offset, Declared: map[string]bool{}}
	for _, tok := range strings.Fields(val) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			continue
		}
		switch k {
		case "kind":
			tr.Kind = v
		case "id":
			tr.TrackID = parseUint32(v)
		case "timescale":
			tr.Timescale = parseUint32(v)
			tr.Declared["timescale"] = true
		case "duration":
			tr.Duration = parseInt64(v)
			tr.Declared["duration"] = true
		case "width":
			tr.Width = parseUint32(v)
			tr.Declared["width"] = true
		case "height":
			tr.Height = parseUint32(v)
			tr.Declared["height"] = true
		case "channels":
			tr.Channels = parseUint32(v)
		case "samplerate":
			tr.SampleRate = parseUint32(v)
		case "language":
			tr.Language = v
		case "rotation":
			tr.Rotation = int(parseInt64(v))
			tr.Declared["rotation"] = true
		case "crop":
			tr.Crop = parseCrop(v)
			tr.Declared["crop"] = true
		case "color":
			tr.Color = parseColor(v)
			tr.Declared["color"] = true
		}
	}
	if tr.Kind == "" {
		rec.Add(model.ErrorRecord{Code: model.ErrInvalidTimestamp, Track: "unknown", Offset: offset,
			Message: "track declaration without kind"})
		return nil
	}
	return tr
}

func parseCrop(s string) Crop {
	parts := strings.Split(s, ",")
	var c Crop
	if len(parts) != 4 {
		return c
	}
	c.Top, c.Bottom, c.Left, c.Right = parseUint32(parts[0]), parseUint32(parts[1]),
		parseUint32(parts[2]), parseUint32(parts[3])
	return c
}

func parseColor(s string) ColorInfo {
	parts := strings.Split(s, ",")
	c := ColorInfo{ColorType: "nclx"}
	if len(parts) >= 3 {
		c.Primaries, c.Transfer, c.Matrix = uint16(parseUint32(parts[0])),
			uint16(parseUint32(parts[1])), uint16(parseUint32(parts[2]))
	}
	if len(parts) == 4 && parts[3] == "1" {
		c.FullRange = true
	}
	return c
}

func parseUint32(s string) uint32 { n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 32); return uint32(n) }
func parseInt64(s string) int64   { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }
