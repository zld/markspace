package manifest

import (
	"bufio"
	"io"
	"math/big"
	"strconv"
	"strings"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

// HLS tag prefixes understood by the fixture subset.
const (
	tagM3U       = "#EXTM3U"
	tagTargetDur = "#EXT-X-TARGETDURATION:"
	tagMediaSeq  = "#EXT-X-MEDIA-SEQUENCE:"
	tagInf       = "#EXTINF:"
	tagStreamInf = "#EXT-X-STREAM-INF:"
)

// ParseHLS parses a fixture-generated HLS master/media playlist. The
// master playlist (one EXT-X-STREAM-INF line per track) and the embedded
// media playlists are delivered together as one reader stream using the
// documented "#EXT-X-FIXTURE-PLAYLIST:<name>" separators.
func ParseHLS(r io.Reader) (*Playlist, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)
	p := &Playlist{Format: FormatHLS}

	var (
		targetTick  int64 = 2
		trackID     uint32
		expectURI   bool
		pendingInf  string
		pendingSeq  uint32
		mediaSeq    uint32 = 1
		mediaSeqSet bool
		inTrack     bool
	)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		loc := "line:" + strconv.Itoa(lineNo)
		if strings.HasPrefix(line, "#EXT-X-FIXTURE-PLAYLIST:") {
			if expectURI {
				return nil, merr.New(merr.KindManifest, FormatHLS, loc,
					"fixture playlist separator in the middle of an EXTINF entry")
			}
			attrs := strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-FIXTURE-PLAYLIST:"))
			fields := strings.Split(attrs, ",")
			name := strings.TrimSpace(fields[0])
			trackID++
			t := &Track{Name: name, ID: trackID, Kind: media.TrackVideo}
			for _, kv := range fields[1:] {
				kv = strings.TrimSpace(kv)
				eq := strings.IndexByte(kv, '=')
				if eq < 0 {
					return nil, merr.New(merr.KindManifest, FormatHLS, loc,
						"malformed playlist attribute: "+kv)
				}
				key, val := strings.TrimSpace(kv[:eq]), strings.TrimSpace(kv[eq+1:])
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					return nil, merr.New(merr.KindManifest, FormatHLS, loc,
						"playlist attribute must be a positive integer: "+kv)
				}
				switch key {
				case "bandwidth":
					t.TargetBitrateBps = n
				case "width":
					t.Width = uint16(n)
				case "height":
					t.Height = uint16(n)
				default:
					return nil, merr.New(merr.KindManifest, FormatHLS, loc,
						"unknown playlist attribute: "+key)
				}
			}
			p.Tracks = append(p.Tracks, t)
			mediaSeq, mediaSeqSet = 1, false
			inTrack = true
			continue
		}
		if line == tagM3U {
			continue
		}
		switch {
		case strings.HasPrefix(line, tagTargetDur):
			v, err := parseTagInt(FormatHLS, loc, line, tagTargetDur)
			if err != nil {
				return nil, err
			}
			if v <= 0 {
				return nil, merr.New(merr.KindManifest, FormatHLS, loc,
					"EXT-X-TARGETDURATION must be positive")
			}
			targetTick = v
		case strings.HasPrefix(line, tagMediaSeq):
			v, err := parseTagInt(FormatHLS, loc, line, tagMediaSeq)
			if err != nil {
				return nil, err
			}
			if v < 1 {
				return nil, merr.New(merr.KindManifest, FormatHLS, loc,
					"illegal EXT-X-MEDIA-SEQUENCE, must be >= 1")
			}
			mediaSeq = uint32(v)
			mediaSeqSet = true
		case strings.HasPrefix(line, tagStreamInf):
			// Master playlist attributes are informational; the embedded
			// playlists carry the authoritative track metadata.
			continue
		case strings.HasPrefix(line, tagInf):
			if !inTrack {
				return nil, merr.New(merr.KindManifest, FormatHLS, loc,
					"EXTINF outside of a fixture playlist section")
			}
			if expectURI {
				return nil, merr.New(merr.KindManifest, FormatHLS, loc,
					"EXTINF without the preceding segment URI")
			}
			pendingInf = strings.TrimPrefix(line, tagInf)
			pendingSeq = mediaSeq
			mediaSeq++
			expectURI = true
		case strings.HasPrefix(line, "#EXT"):
			// Unknown tags (ENDLIST, VERSION, ...) are tolerated.
			continue
		default:
			if !inTrack {
				return nil, merr.New(merr.KindManifest, FormatHLS, loc,
					"segment URI outside of a fixture playlist section")
			}
			if !expectURI {
				return nil, merr.New(merr.KindManifest, FormatHLS, loc,
					"segment URI without preceding EXTINF")
			}
			ref, err := parseInf(FormatHLS, loc, pendingInf, line, pendingSeq)
			if err != nil {
				return nil, err
			}
			t := p.Tracks[len(p.Tracks)-1]
			t.Refs = append(t.Refs, ref)
			expectURI = false
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, merr.Wrap(err, FormatHLS, "scanner", "failed reading HLS playlist")
	}
	if expectURI {
		return nil, merr.New(merr.KindManifest, FormatHLS, "end-of-file",
			"dangling EXTINF without segment URI")
	}
	if !mediaSeqSet && len(p.Tracks) > 0 && len(p.Tracks[0].Refs) > 0 &&
		p.Tracks[0].Refs[0].Seq != 1 {
		return nil, merr.New(merr.KindManifest, FormatHLS, "playlist",
			"segment sequence does not start at 1")
	}
	p.TargetDurationTick = targetTick
	if err := validate(p); err != nil {
		return nil, err
	}
	return p, nil
}

// parseInf parses "10.000,title" into an exact rational duration.
func parseInf(format, loc, raw, uri string, seq uint32) (SegmentRef, error) {
	durPart := raw
	if comma := strings.IndexByte(raw, ','); comma >= 0 {
		durPart = raw[:comma]
	}
	durPart = strings.TrimSpace(durPart)
	rat, ok := new(big.Rat).SetString(durPart)
	if !ok || rat.Sign() <= 0 {
		return SegmentRef{}, merr.New(merr.KindManifest, format, loc,
			"EXTINF missing or invalid duration: "+raw)
	}
	return SegmentRef{
		Seq:         seq,
		URI:         uri,
		DurationNum: rat.Num().Int64(),
		DurationDen: rat.Denom().Int64(),
		RawDuration: durPart,
	}, nil
}

func parseTagInt(format, loc, line, tag string) (int64, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(line, tag))
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, merr.New(merr.KindManifest, format, loc,
			"malformed integer tag "+tag+": "+raw)
	}
	return v, nil
}
