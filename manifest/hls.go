package manifest

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"mediadist/timeline"
)

// HLSVariant is one rendition in an HLS master/playlist fixture.
type HLSVariant struct {
	URI       string
	MediaSeq  uint64
	Epochs    []HLSEpoch
	Segments  []Segment
}

// HLSEpoch groups segments between two discontinuities. Media sequence
// numbers keep increasing across epochs; the epoch index resets timeline
// context and forbids cross-epoch stitching.
type HLSEpoch struct {
	Index     int
	StartSeq  uint64
	SegmentN  int
	StartTime timeline.Rat
}

// Playlist is the parsed HLS media playlist.
type Playlist struct {
	TargetDurationMs int64
	Variants         []*HLSVariant
}

const maxPlaylistLines = 10000

// ParseM3U8 parses a simplified local HLS fixture:
//
//	#EXTM3U
//	#EXT-X-TARGETDURATION:<seconds>
//	#EXT-X-MEDIA-SEQUENCE:<n>
//	#EXTINF:<seconds>,
//	segment-uri
//	#EXT-X-DISCONTINUITY
//
// A discontinuity starts a new epoch while preserving the running media
// sequence counter.
func ParseM3U8(data []byte) (*Playlist, error) {
	if len(data) > MaxManifestBytes {
		return nil, timeline.NewError("MANIFEST_TOO_LARGE", "M3U8 exceeds size limit",
			map[string]any{"bytes": len(data), "limit": MaxManifestBytes})
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxAttributeLen*2)
	pl := &Playlist{}
	v := &HLSVariant{URI: "edge-0"}
	var (
		lineNo      int
		nextSeq     uint64
		seqKnown    bool
		pendingInf  bool
		pendingDur  int64
		cursor      timeline.Rat
	)
	for scanner.Scan() {
		lineNo++
		if lineNo > maxPlaylistLines {
			return nil, timeline.NewError("MANIFEST_LIMIT", "playlist line limit exceeded",
				map[string]any{"limit": maxPlaylistLines})
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch {
		case line == "#EXTM3U":
			continue
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			n, err := parseSuffixInt(line, lineNo)
			if err != nil {
				return nil, err
			}
			pl.TargetDurationMs = n * 1000
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			n, err := parseSuffixInt(line, lineNo)
			if err != nil {
				return nil, err
			}
			nextSeq, seqKnown = uint64(n), true
		case line == "#EXT-X-DISCONTINUITY":
			if pendingInf {
				return nil, badLine(lineNo, line, "dangling EXTINF before discontinuity")
			}
			openEpoch(v, cursor)
		case strings.HasPrefix(line, "#EXTINF:"):
			dur, err := parseExtInf(line, lineNo)
			if err != nil {
				return nil, err
			}
			pendingInf, pendingDur = true, dur
		case strings.HasPrefix(line, "#"):
			// Unknown tags are ignored in the local fixture grammar.
			continue
		default:
			if !pendingInf {
				return nil, badLine(lineNo, line, "segment URI without EXTINF")
			}
			if !seqKnown {
				return nil, timeline.NewError("MANIFEST_MALFORMED",
					"missing EXT-X-MEDIA-SEQUENCE", map[string]any{"line": lineNo})
			}
			if len(v.Segments) == 0 {
				v.Epochs = append(v.Epochs, HLSEpoch{
					Index: 0, StartSeq: nextSeq, StartTime: cursor,
				})
			}
			dur := timeline.MustNewRat(pendingDur, 1000)
			epochIdx := len(v.Epochs) - 1
			seg := Segment{
				Epoch: epochIdx, Index: len(v.Segments),
				Start: cursor, Duration: dur, URI: line,
			}
			v.Segments = append(v.Segments, seg)
			v.Epochs[epochIdx].SegmentN++
			advanced, err := cursor.Add(dur)
			if err != nil {
				return nil, err
			}
			cursor = advanced
			nextSeq++
			pendingInf = false
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, timeline.NewError("MANIFEST_MALFORMED", "scanner failure",
			map[string]any{"error": err.Error()})
	}
	if pendingInf {
		return nil, timeline.NewError("MANIFEST_MALFORMED", "trailing EXTINF without segment", nil)
	}
	if len(v.Segments) == 0 {
		return nil, timeline.NewError("MANIFEST_MALFORMED", "playlist contains no segments", nil)
	}
	v.MediaSeq = v.Epochs[0].StartSeq
	pl.Variants = append(pl.Variants, v)
	return pl, nil
}

func openEpoch(v *HLSVariant, at timeline.Rat) {
	idx := len(v.Epochs)
	startSeq := v.MediaSeq
	if idx > 0 {
		prev := v.Epochs[idx-1]
		startSeq = prev.StartSeq + uint64(prev.SegmentN)
	}
	v.Epochs = append(v.Epochs, HLSEpoch{
		Index: idx, StartSeq: startSeq, StartTime: at,
	})
}

func parseSuffixInt(line string, lineNo int) (int64, error) {
	raw := line[strings.IndexByte(line, ':')+1:]
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, badLine(lineNo, line, "integer attribute unparsable")
	}
	return n, nil
}

func parseExtInf(line string, lineNo int) (int64, error) {
	rest := line[len("#EXTINF:"):]
	raw, _, _ := strings.Cut(rest, ",")
	secs, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || secs <= 0 {
		return 0, badLine(lineNo, line, "EXTINF duration invalid")
	}
	return int64(secs*1000 + 0.5), nil
}

// CrossEpochStitchError verifies that two adjacent segments may be joined
// inside one output timeline. Joining across discontinuity epochs is
// always rejected so timelines never splice incompatible samples.
func CrossEpochStitchError(a, b Segment) error {
	if a.Epoch != b.Epoch {
		return timeline.NewError("CROSS_EPOCH_STITCH",
			"segments from different discontinuity epochs cannot be stitched",
			map[string]any{"epoch_a": a.Epoch, "epoch_b": b.Epoch,
				"uri_a": a.URI, "uri_b": b.URI})
	}
	return nil
}
