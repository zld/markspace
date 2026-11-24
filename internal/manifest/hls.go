package manifest

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediad/internal/model"
)

// AlignmentThresholdNS is the maximum permitted representation offset.
// Pairs above it are recorded as misaligned and never merged downstream.
const AlignmentThresholdNS model.PlayTime = 20_000_000

// hlsTimeScale makes one tick exactly one nanosecond, so EXTINF decimals
// convert without rounding and the timeline stays exactly reconstructible.
const hlsTimeScale model.TimeScale = 1_000_000_000

type hlsEntry struct {
	uri            string
	durationNS     model.PlayTime
	durationRaw    string
	discontinuity  bool
	mediaSequence  int64
}

// parseDecimalSeconds converts "2", "2.5", "2.500000000" to nanoseconds
// exactly. More than nine fractional digits is a field-range error.
func parseDecimalSeconds(s string) (model.PlayTime, error) {
	parts := strings.SplitN(s, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, fmt.Errorf("invalid EXTINF seconds %q", s)
	}
	ns := whole * 1_000_000_000
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 9 {
			return 0, fmt.Errorf("EXTINF %q exceeds nanosecond precision", s)
		}
		f, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid EXTINF fraction %q", s)
		}
		pow := int64(1)
		for i := 0; i < 9-len(frac); i++ {
			pow *= 10
		}
		ns += f * pow
	}
	return model.PlayTime(ns), nil
}

func isMasterPlaylist(lines [][]byte) bool {
	for _, l := range lines {
		if bytes.HasPrefix(l, []byte("#EXT-X-STREAM-INF")) {
			return true
		}
	}
	return false
}

// ParseHLS parses an HLS playlist (master or media). Child playlists of a
// master are fetched through resolve so the parser stays file-system free.
func ParseHLS(data []byte, path string, resolve func(uri string) ([]byte, error)) (*model.Manifest, error) {
	var lines [][]byte
	for sc := bufio.NewScanner(bytes.NewReader(data)); sc.Scan(); {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	if len(lines) == 0 || !bytes.Equal(lines[0], []byte("#EXTM3U")) {
		return nil, model.NewError(model.KindCorruptBox, loc(path, "", -1),
			"playlist missing #EXTM3U header")
	}
	if isMasterPlaylist(lines) {
		return parseMaster(lines, path, resolve)
	}
	rep, total, err := parseMedia(lines, path, "default")
	if err != nil {
		return nil, err
	}
	m := &model.Manifest{
		Kind: "hls", Path: path, TimeScale: hlsTimeScale, Duration: total,
		Representations: []model.Representation{rep},
	}
	return m, nil
}

func parseMedia(lines [][]byte, path, repID string) (model.Representation, model.PlayTime, error) {
	entries := []hlsEntry{}
	var pendingInf string
	pending := false
	disc := false
	var seq int64
	for _, bline := range lines {
		line := string(bline)
		switch {
		case line == "#EXT-X-DISCONTINUITY":
			disc = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err != nil || v < 0 {
				return model.Representation{}, 0, model.NewError(model.KindFieldOutOfRange,
					loc(path, repID, -1), "invalid media sequence")
			}
			seq = v
		case strings.HasPrefix(line, "#EXTINF:"):
			pendingInf = strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(pendingInf, ','); i >= 0 {
				pendingInf = pendingInf[:i]
			}
			pending = true
		case strings.HasPrefix(line, "#"):
			// other tags (KEY, VERSION, TARGETDURATION...) are tolerated
		default:
			if !pending {
				return model.Representation{}, 0, model.NewError(model.KindMissingDuration,
					loc(path, repID, len(entries)),
					"segment URI without preceding EXTINF")
			}
			dur, err := parseDecimalSeconds(pendingInf)
			if err != nil {
				return model.Representation{}, 0, model.NewError(model.KindFieldOutOfRange,
					loc(path, repID, len(entries)), err.Error())
			}
			entries = append(entries, hlsEntry{
				uri: line, durationNS: dur, durationRaw: pendingInf,
				discontinuity: disc, mediaSequence: seq + int64(len(entries)),
			})
			disc = false
			pending = false
		}
	}
	if len(entries) == 0 {
		return model.Representation{}, 0, model.NewError(model.KindMissingDuration,
			loc(path, repID, -1), "media playlist has no segments")
	}
	segs := make([]model.SegmentRef, 0, len(entries))
	var tick model.PlayTime
	for i, e := range entries {
		segs = append(segs, model.SegmentRef{
			Index: i, URL: e.uri,
			StartTicks: int64(tick), Start: tick,
			DurationTicks: int64(e.durationNS), Duration: e.durationNS,
			Discontinuity: e.discontinuity,
		})
		next, err := model.Add(tick, e.durationNS)
		if err != nil {
			return model.Representation{}, 0, model.NewError(model.KindFieldOutOfRange,
				loc(path, repID, i), "playlist duration accumulation overflow")
		}
		tick = next
	}
	return model.Representation{
		ID: repID, Kind: model.TrackVideo, TimeScale: hlsTimeScale, Segments: segs,
	}, tick, nil
}

func parseMaster(lines [][]byte, path string, resolve func(uri string) ([]byte, error)) (*model.Manifest, error) {
	if resolve == nil {
		return nil, model.NewError(model.KindManifestMissing, loc(path, "", -1),
			"master playlist requires a resolver")
	}
	m := &model.Manifest{Kind: "hls", Path: path, TimeScale: hlsTimeScale}
	var nextID string
	for _, bline := range lines {
		line := string(bline)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			nextID = streamAttr(line, "BANDWIDTH")
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if nextID == "" {
			nextID = "variant"
		}
		child, err := resolve(line)
		if err != nil {
			return nil, model.NewError(model.KindManifestMissing, loc(path, nextID, -1),
				"cannot resolve variant "+line+": "+err.Error())
		}
		rep, err := ParseHLS(child, filepath.Join(filepath.Dir(path), line), resolve)
		if err != nil {
			return nil, err
		}
		r := rep.Representations[0]
		r.ID = nextID
		m.Representations = append(m.Representations, r)
		total := r.Segments[len(r.Segments)-1].Start + r.Segments[len(r.Segments)-1].Duration
		if total > m.Duration {
			m.Duration = total
		}
		nextID = ""
	}
	if len(m.Representations) == 0 {
		return nil, model.NewError(model.KindMissingDuration, loc(path, "", -1),
			"master playlist lists no variants")
	}
	CheckAlignment(m)
	return m, nil
}

// CheckAlignment marks every representation pair whose per-index segment
// starts differ by more than AlignmentThresholdNS.
func CheckAlignment(m *model.Manifest) {
	for i := 0; i < len(m.Representations); i++ {
		for j := i + 1; j < len(m.Representations); j++ {
			a, b := m.Representations[i], m.Representations[j]
			n := len(a.Segments)
			if len(b.Segments) < n {
				n = len(b.Segments)
			}
			for k := 0; k < n; k++ {
				d := a.Segments[k].Start - b.Segments[k].Start
				if d < 0 {
					d = -d
				}
				if d > AlignmentThresholdNS {
					m.Misaligned = append(m.Misaligned, model.Misalignment{
						RepA: a.ID, RepB: b.ID, OffsetNS: d,
					})
					break
				}
			}
		}
	}
}

func streamAttr(line, name string) string {
	i := strings.Index(line, name+"=")
	if i < 0 {
		return ""
	}
	v := line[i+len(name)+1:]
	if strings.HasPrefix(v, "\"") {
		v = strings.TrimPrefix(v, "\"")
		if e := strings.IndexByte(v, '"'); e >= 0 {
			return v[:e]
		}
	}
	if e := strings.IndexAny(v, ",\n"); e >= 0 {
		return v[:e]
	}
	return v
}

// ParseHLSFile opens a playlist and resolves child URIs next to it.
func ParseHLSFile(path string) (*model.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.NewError(model.KindManifestMissing, loc(path, "", -1), err.Error())
	}
	dir := filepath.Dir(path)
	return ParseHLS(data, path, func(uri string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, uri))
	})
}
