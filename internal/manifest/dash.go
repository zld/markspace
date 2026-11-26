package manifest

import (
	"fmt"
	"strings"

	"medtrace/internal/core"
)

// parseDASH parses a static MPD. It supports the shape a local
// multi-representation VOD package uses: one Period, AdaptationSets with one
// or more Representations, an initialization segment and a SegmentTemplate
// addressed by $Number$ or $Time$ (with or without a SegmentTimeline).
// Anything beyond that (SegmentList, BaseURL redirection, multiple Periods)
// is rejected with a positioned error rather than silently producing an index
// that does not match the media.
func parseDASH(rel string, data []byte) (*Playlist, error) {
	root, f := buildTree(rel, data)
	if f != nil {
		return nil, f
	}
	if root.name != "MPD" {
		return nil, dashFault(rel, root.line, fmt.Sprintf("root element is %q, want MPD", root.name))
	}
	mpdDur, err := mpdDuration(rel, root)
	if err != nil {
		return nil, err
	}
	period := root.child("Period")
	if period == nil {
		return nil, dashFault(rel, root.line, "MPD has no Period")
	}
	if all := root.childrenNamed("Period"); len(all) > 1 {
		return nil, dashFault(rel, all[1].line, "multiple Periods are not supported in a static local package")
	}
	sets := period.childrenNamed("AdaptationSet")
	if len(sets) == 0 {
		return nil, dashFault(rel, period.line, "Period has no AdaptationSet")
	}
	pl := &Playlist{Kind: DASHKind, File: rel}
	for _, set := range sets {
		kind := trackKindOf(set)
		base, f := setTemplate(rel, set)
		if f != nil {
			return nil, f
		}
		for _, rep := range set.childrenNamed("Representation") {
			if f := appendRepresentation(pl, rel, set, rep, base, kind, mpdDur); f != nil {
				return nil, f
			}
		}
	}
	if len(pl.Representations) == 0 {
		return nil, dashFault(rel, period.line, "no Representation found")
	}
	if len(pl.Segments) == 0 {
		return nil, dashFault(rel, period.line, "no media segments found")
	}
	return pl, nil
}

func dashFault(rel string, line int, detail string) *core.Fault {
	return core.NewFault(core.KindManifest, core.Pos{File: rel, Line: line, Byte: -1}, "dash", detail)
}

// mpdDuration reads mediaPresentationDuration, used to bound segment
// enumeration when a template declares a duration rather than a timeline.
func mpdDuration(rel string, root *node) (core.Nanos, error) {
	s, ok := root.attr("mediaPresentationDuration")
	if !ok || strings.TrimSpace(s) == "" {
		return 0, nil
	}
	d, err := parseISODuration(s)
	if err != nil {
		return 0, dashFault(rel, root.line, err.Error())
	}
	return d, nil
}

// trackKindOf infers an AdaptationSet's media type from contentType, mimeType
// or codecs, in that order.
func trackKindOf(n *node) core.TrackKind {
	if v, ok := n.attr("contentType"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "video":
			return core.Video
		case "audio":
			return core.Audio
		}
	}
	if v, ok := n.attr("mimeType"); ok {
		v = strings.ToLower(v)
		switch {
		case strings.HasPrefix(v, "video/"):
			return core.Video
		case strings.HasPrefix(v, "audio/"):
			return core.Audio
		}
	}
	if v, ok := n.attr("codecs"); ok {
		switch {
		case strings.HasPrefix(v, "avc"), strings.HasPrefix(v, "hvc"),
			strings.HasPrefix(v, "vp0"), strings.HasPrefix(v, "av01"):
			return core.Video
		case strings.HasPrefix(v, "mp4a"), strings.HasPrefix(v, "opus"),
			strings.HasPrefix(v, "ac-3"):
			return core.Audio
		}
	}
	return core.Other
}
