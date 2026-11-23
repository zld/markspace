package demux

import (
	"encoding/binary"

	"github.com/abema/go-mp4"

	"mediadist/timeline"
)

// mdst is the fixture metadata box (unregistered fourCC "mdst"):
//
//	u8  version
//	u8  layout       (ChannelLayout code; 255 = unsupported)
//	u16 sample_rate
//	u8  color_source (0 none | 1 container | 2 bitstream | 3 cli)
//	u8  reserved
//	u16 reserved
const mdstBoxType = "mdst"

// trackIndex groups all boxes belonging to one trak by traversal order.
type trackIndex struct {
	trackID uint32
	kind    timeline.Kind
	boxes   []rawBox
}

func buildGraph(raws []rawBox) (*timeline.Graph, error) {
	groups := groupTracks(raws)
	var graph timeline.Graph
	for _, grp := range groups {
		tr, err := buildTrack(grp)
		if err != nil {
			return nil, err
		}
		graph.Tracks = append(graph.Tracks, tr)
	}
	if len(graph.Tracks) == 0 {
		return nil, timeline.NewLocError("GRAPH_EMPTY", timeline.StageDemux, 0, -1, "moov/trak", nil)
	}
	return &graph, nil
}

func groupTracks(raws []rawBox) []trackIndex {
	// The structural walk emits boxes in depth-first post-order: a trak
	// container event arrives only after all its descendants. Scanning in
	// reverse therefore makes each trak event precede exactly its own subtree,
	// giving an unambiguous partition without sibling indices in the path.
	var reversed []rawBox
	for i := len(raws) - 1; i >= 0; i-- {
		reversed = append(reversed, raws[i])
	}
	var groups []trackIndex
	var cur *trackIndex
	for _, rb := range reversed {
		if boxType(rb) == "trak" {
			groups = append(groups, trackIndex{})
			cur = &groups[len(groups)-1]
			continue
		}
		if cur == nil || !pathContains(rb.path, "trak") {
			continue
		}
		cur.boxes = append(cur.boxes, rb)
		if tkhd, ok := rb.box.(*mp4.Tkhd); ok {
			cur.trackID = tkhd.TrackID
		}
		if hdlr, ok := rb.box.(*mp4.Hdlr); ok {
			switch string(hdlr.HandlerType[:]) {
			case "vide":
				cur.kind = timeline.KindVideo
			case "soun":
				cur.kind = timeline.KindAudio
			}
		}
	}
	// Boxes within a group were collected in reverse; restore document order.
	for i := range groups {
		for lo, hi := 0, len(groups[i].boxes)-1; lo < hi; lo, hi = lo+1, hi-1 {
			groups[i].boxes[lo], groups[i].boxes[hi] = groups[i].boxes[hi], groups[i].boxes[lo]
		}
	}
	// Groups themselves are also reversed; restore trak document order.
	for lo, hi := 0, len(groups)-1; lo < hi; lo, hi = lo+1, hi-1 {
		groups[lo], groups[hi] = groups[hi], groups[lo]
	}
	return groups
}

func pathContains(path []mp4.BoxType, fourCC string) bool {
	for _, b := range path {
		if b.String() == fourCC {
			return true
		}
	}
	return false
}

func buildTrack(grp trackIndex) (*timeline.Track, error) {
	t := &timeline.Track{ID: grp.trackID, Kind: grp.kind}
	var mdhd *mp4.Mdhd
	var hasMdst bool
	// First pass: mdst is the authoritative fixture metadata and must win over
	// the conventional mp4a defaults (which cannot express 5.1 or faults).
	for _, rb := range grp.boxes {
		if b, ok := rb.box.(*MdstBox); ok {
			applyMdst(t, b.Payload[:])
			hasMdst = true
		}
	}
	for _, rb := range grp.boxes {
		switch b := rb.box.(type) {
		case *mp4.Mdhd:
			mdhd = b
		case *mp4.AudioSampleEntry:
			if !hasMdst {
				t.Layout = layoutFromMp4a(b.ChannelCount)
				t.SampleRate = uint32(b.SampleRate >> 16) // fixed-point 16.16
			}
		case *mp4.Colr:
			applyColr(t, b)
		}
	}
	if mdhd == nil {
		return nil, timeline.NewLocError("MDHD_MISSING", timeline.StageDemux, t.ID, -1, "mdhd", nil)
	}
	t.Timescale = mdhd.Timescale
	clock, err := timeline.NewClock(mdhd.Timescale)
	if err != nil {
		return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageDemux, t.ID, -1, "timescale", err)
	}
	if err := expandSamples(t, grp.boxes, clock); err != nil {
		return nil, err
	}
	return t, nil
}

func layoutFromMp4a(channels uint16) timeline.ChannelLayout {
	switch channels {
	case 1:
		return timeline.LayoutMono
	case 2:
		return timeline.LayoutStereo
	case 6:
		return timeline.LayoutFiveOne
	default:
		return timeline.LayoutUnsupported
	}
}

func applyColr(t *timeline.Track, c *mp4.Colr) {
	desc := timeline.ColorDesc{
		Primaries: c.ColourPrimaries, Transfer: c.TransferCharacteristics,
		Matrix: c.MatrixCoefficients, FullRange: c.FullRangeFlag,
		Source: timeline.SourceContainer,
	}
	if t.ColorCandidates == nil {
		t.ColorCandidates = map[timeline.ColorSource]timeline.ColorDesc{}
	}
	t.ColorCandidates[timeline.SourceContainer] = desc
}

func applyMdst(t *timeline.Track, p []byte) {
	if len(p) < 8 {
		return
	}
	if t.Kind == timeline.KindAudio {
		t.Layout = timeline.ChannelLayout(p[1])
		t.SampleRate = uint32(binary.BigEndian.Uint16(p[2:4]))
	}
	if t.Kind == timeline.KindVideo {
		src := []timeline.ColorSource{
			timeline.SourceNone, timeline.SourceContainer,
			timeline.SourceBitstream, timeline.SourceCLI,
		}[p[4]]
		// Bitstream-sourced color lives in mdst; container color comes from colr.
		if src == timeline.SourceBitstream {
			if t.ColorCandidates == nil {
				t.ColorCandidates = map[timeline.ColorSource]timeline.ColorDesc{}
			}
			// Values mirror the colr box when both are present, or carry the
			// only description when colr is absent.
			if existing, ok := t.ColorCandidates[timeline.SourceContainer]; ok {
				existing.Source = timeline.SourceBitstream
				t.ColorCandidates[timeline.SourceBitstream] = existing
			} else if len(p) >= 8 {
				t.ColorCandidates[timeline.SourceBitstream] = timeline.ColorDesc{
					Primaries: binary.BigEndian.Uint16(p[6:8]), Source: timeline.SourceBitstream,
				}
			}
		}
	}
}

func boxType(rb rawBox) string {
	return rb.info.Type.String()
}

// findBox returns the first box of the given fourCC inside the track group.
func findBox(boxes []rawBox, fourCC string) *rawBox {
	for i := range boxes {
		if boxType(boxes[i]) == fourCC {
			return &boxes[i]
		}
	}
	return nil
}
