// Package maptrack validates source tracks and builds deterministic output
// track mappings without transcoding or changing codec timing.
package maptrack

import (
	"sort"

	"mediacore/internal/fault"
	"mediacore/internal/scan"
)

// Supported codecs are intentionally explicit. Adding a four-character code
// here without a real scanner/writer is forbidden.
var supportedCodecs = map[string]string{
	"mp4v": "video",
	"mcrv": "video", // generated RTP video payload profile
	"mp4a": "audio",
	"mcau": "audio", // generated RTP audio payload profile
}

// Map builds identity/lossless output mappings in stable source order.
func Map(idx *scan.Index) ([]scan.TrackMapping, error) {
	if idx == nil {
		return nil, fault.New(fault.InvalidArgument, "nil index")
	}
	tracks := append([]scan.Track(nil), idx.Tracks...)
	sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].ID < tracks[j].ID })

	mappings := make([]scan.TrackMapping, 0, len(tracks))
	var nextOut uint32 = 1
	for _, tr := range tracks {
		kind, ok := supportedCodecs[tr.Codec]
		if !ok {
			return nil, fault.New(fault.UnsupportedCodec,
				"refusing unknown/unsupported codec "+tr.Codec)
		}
		if tr.Kind != "" && tr.Kind != kind {
			return nil, fault.New(fault.UnsupportedCodec,
				"codec/handler kind mismatch for "+tr.Codec)
		}
		if tr.Timescale == 0 {
			return nil, fault.New(fault.CorruptHeader, "track timescale is zero")
		}
		action := "copy"
		if idx.Format == scan.FormatRTP {
			action = "depacketize-copy"
		}
		ssrc := uint32(0)
		for _, rs := range idx.RTPStreams {
			if rs.TrackID == tr.ID {
				ssrc = rs.SSRC
				break
			}
		}
		mappings = append(mappings, scan.TrackMapping{
			SourceTrackID: tr.ID,
			OutputTrackID: nextOut,
			SSRC:          ssrc,
			SourceCodec:   tr.Codec,
			Codec:         normalizeCodec(tr.Codec),
			Kind:          kind,
			Action:        action,
			OffsetNS:      tr.InitialOffsetNS,
		})
		nextOut++
	}
	if len(mappings) == 0 {
		return nil, fault.New(fault.UnsupportedFormat, "no supported tracks found")
	}
	idx.Mapping = mappings
	return mappings, nil
}

// BySource returns the mapping for a source track ID.
func BySource(m []scan.TrackMapping, id uint32) (scan.TrackMapping, bool) {
	for _, mp := range m {
		if mp.SourceTrackID == id {
			return mp, true
		}
	}
	return scan.TrackMapping{}, false
}

// BySSRC returns the mapping for an RTP SSRC.
func BySSRC(m []scan.TrackMapping, ssrc uint32) (scan.TrackMapping, bool) {
	for _, mp := range m {
		if mp.SSRC == ssrc {
			return mp, true
		}
	}
	return scan.TrackMapping{}, false
}

func normalizeCodec(codec string) string {
	switch codec {
	case "mcrv":
		return "mp4v"
	case "mcau":
		return "mp4a"
	default:
		return codec
	}
}
