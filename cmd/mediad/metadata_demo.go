package main

import (
	"sort"

	"mediad/internal/meta"
)

// buildMetadata generates valid and corrupt MXF/MP4 fixtures, parses both,
// arbitrates conflicts and returns the merged result plus every error code
// observed. Corrupt tracks must not prevent independent tracks from parsing.
func buildMetadata(r *rng) (*meta.Result, []string) {
	codes := map[string]bool{}
	record := func(res *meta.Result) {
		for _, e := range res.Errors {
			codes[string(e.Code)] = true
		}
	}

	// --- MXF: one healthy video track with authored color/crop/rotation.
	mxfTracks := []meta.MXFInput{
		{
			TrackNo: 1, Kind: "video", ClockNum: 90000, ClockDen: 1,
			Width: 1920, Height: 1080, Rotation: 90,
			Crop:  meta.Crop{X: 0, Y: 0, Width: 1280, Height: 720, Source: meta.SourceMXF},
			Color: meta.Color{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Source: meta.SourceMXF},
		},
		{
			TrackNo: 2, Kind: "audio", ClockNum: 48000, ClockDen: 1,
			Crop:  meta.FullFrame(0, 0),
			Color: meta.Color{Source: meta.SourceMXF},
		},
	}
	// Healthy stream first; corrupt streams are parsed as independent inputs
	// so each corruption yields its structured code without losing good tracks.
	mxfRes := meta.ParseMXF(meta.BuildMXF(mxfTracks))
	record(mxfRes)
	for _, corrupt := range [][]byte{
		meta.CorruptMXFInvalidCrop(),
		meta.CorruptMXFBadBER(),
	} {
		record(meta.ParseMXF(corrupt))
	}

	// --- MP4: same tracks with deliberately conflicting rotation/crop/color,
	// plus an MP4-only track and two corrupt containers.
	mp4Tracks := []meta.MP4Input{
		{
			TrackNo: 1, Kind: "video", ClockNum: 90000, Width: 1920, Height: 1080,
			Rotation: 270,
			Crop:  meta.Crop{X: 16, Y: 16, Width: 960, Height: 540, Source: meta.SourceMP4},
			Color: meta.Color{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Source: meta.SourceMP4},
		},
		{TrackNo: 2, Kind: "audio", ClockNum: 48000, OmitCrop: true, OmitColor: true},
		{TrackNo: 4, Kind: "video", ClockNum: 30000, Width: 640, Height: 360,
			Rotation: r.intn(2) * 90,
			Crop:     meta.Crop{Width: 640, Height: 360, Source: meta.SourceMP4},
			Color:    meta.Color{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Source: meta.SourceMP4}},
	}
	// Healthy stream and corrupt streams are parsed independently: a broken
	// neighbor never takes down a valid track, mirroring multi-file ingest.
	mp4Res := meta.ParseMP4(meta.BuildMP4(mp4Tracks))
	record(mp4Res)
	for _, corrupt := range [][]byte{
		meta.CorruptMP4InvalidCrop(),
		meta.CorruptMP4TruncatedHeader(),
	} {
		bad := meta.ParseMP4(corrupt)
		record(bad)
	}

	merged := meta.ResolveAll(mxfRes, mp4Res)
	return merged, sortedKeys(codes)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
