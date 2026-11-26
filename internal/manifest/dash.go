package manifest

import (
	"bytes"
	"encoding/xml"
	"io"
	"strconv"

	"continuity/internal/core"
	"continuity/internal/timebase"
)

// ParseDASH parses a static MPD using SegmentList. Segment byte offsets in
// the XML are converted to 1-based line numbers; other addressing modes
// (SegmentTemplate, BaseURL-only) are rejected as unsupported.
func ParseDASH(path string) (*Playlist, error) {
	data, err := readLimited(path, MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	dir := dirOf(path)
	pl := &Playlist{Path: path, Dir: dir}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var timescale, segDur, startNumber int64 = 1, 0, 0
	var inMPD, inSegList bool
	var idx int64
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path,
				lineOf(data, dec.InputOffset()), -1, "xml", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		line := lineOf(data, dec.InputOffset())
		switch se.Name.Local {
		case "MPD":
			inMPD = true
			if t := attr(se, "type"); t != "" && t != "static" {
				return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, line, -1,
					"only static MPD supported, got type="+t, nil)
			}
		case "SegmentList":
			inSegList = true
			timescale = attrInt(se, "timescale", 1)
			segDur = attrInt(se, "duration", 0)
			startNumber = attrInt(se, "startNumber", 0)
			idx = startNumber
		case "SegmentURL":
			if !inSegList {
				continue
			}
			media := attr(se, "media")
			rel, err := resolveURI(dir, media, path, line)
			if err != nil {
				return nil, err
			}
			dur := int64(0)
			if segDur > 0 && timescale > 0 {
				dur = segDur * timebase.TicksPerSecond / timescale
			}
			pl.Segments = append(pl.Segments, Segment{URI: rel, Line: line, Index: idx, DurTicks: dur})
			idx++
		}
	}
	if !inMPD {
		return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, 1, -1,
			"missing MPD root", nil)
	}
	if len(pl.Segments) == 0 {
		return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, 0, -1,
			"no SegmentURL entries", nil)
	}
	return pl, nil
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func attrInt(se xml.StartElement, name string, def int64) int64 {
	v := attr(se, name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// lineOf converts a byte offset in the document to a 1-based line number.
func lineOf(data []byte, off int64) int {
	if off > int64(len(data)) {
		off = int64(len(data))
	}
	return 1 + bytes.Count(data[:off], []byte{'\n'})
}
